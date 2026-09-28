// Package webhook handles inbound gowa webhook deliveries: verifies the
// HMAC signature, filters to allowed senders, and hands text messages off
// to the claude runner — replying asynchronously since gowa expects a 2xx
// response within 10s and Claude can take much longer than that.
//
// Every message that passes the filters is written to a durable pending
// queue (internal/pending) before being ack'd, and removed only once a
// reply has actually been sent — so a bridge crash mid-flight leaves
// something for main.go to replay on the next startup instead of silently
// dropping the message.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/access"
	"github.com/tarkiman/claude-whatsapp/internal/claude"
	"github.com/tarkiman/claude-whatsapp/internal/config"
	"github.com/tarkiman/claude-whatsapp/internal/gowa"
	"github.com/tarkiman/claude-whatsapp/internal/pending"
	"github.com/tarkiman/claude-whatsapp/internal/session"
	"github.com/tarkiman/claude-whatsapp/internal/transcribe"
)

type event struct {
	Event string `json:"event"`
	// DeviceID is the JID of the bot account that received the event.
	DeviceID string  `json:"device_id"`
	Payload  payload `json:"payload"`
}

type payload struct {
	ID       string `json:"id"`
	ChatID   string `json:"chat_id"`
	From     string `json:"from"`
	FromLID  string `json:"from_lid"`
	FromName string `json:"from_name"`
	IsFromMe bool   `json:"is_from_me"`
	Body     string `json:"body"`

	// At most one of these is set per message — see media.go.
	Image    *mediaRef `json:"image"`
	Video    *mediaRef `json:"video"`
	Document *mediaRef `json:"document"`
	Sticker  *mediaRef `json:"sticker"`
	Audio    *mediaRef `json:"audio"`
}

type Handler struct {
	cfg        *config.Config
	gowa       *gowa.Client
	runner     *claude.Runner
	store      *session.Store
	pending    *pending.Store
	transcribe *transcribe.Transcriber // nil disables voice-note transcription
	chatLocks  *chatLocks
	access     *access.Store

	lidMu    sync.Mutex
	lidCache map[string]lidEntry // team group JID -> the bot's LID in that group
}

type lidEntry struct {
	lid string
	at  time.Time
}

func New(cfg *config.Config, gowaClient *gowa.Client, runner *claude.Runner, store *session.Store, pendingStore *pending.Store, transcriber *transcribe.Transcriber, accessStore *access.Store) *Handler {
	return &Handler{cfg: cfg, gowa: gowaClient, runner: runner, store: store, pending: pendingStore, transcribe: transcriber, chatLocks: newChatLocks(), access: accessStore, lidCache: map[string]lidEntry{}}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	if !h.validSignature(req.Header.Get("X-Hub-Signature-256"), body) {
		log.Printf("webhook: rejected — bad signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var e event
	if err := json.Unmarshal(body, &e); err != nil {
		log.Printf("webhook: rejected — bad json: %v", err)
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	mediaType, ref := attachment(&e.Payload)

	if e.Event != "message" || e.Payload.IsFromMe || (e.Payload.Body == "" && mediaType == "") {
		w.WriteHeader(http.StatusOK)
		return
	}
	dec := h.decide(&e, mediaType != "")
	if !dec.Allow {
		if !dec.Quiet {
			log.Printf("webhook: ignoring message reason=%s sender=%s chat=%s", dec.Reason, e.Payload.From, e.Payload.ChatID)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	mediaPath := resolveMediaPath(h.cfg, ref)
	prompt := buildPrompt(dec.Body, mediaType, mediaPath, ref)
	if dec.Team {
		// One Claude session is shared by the whole group, so it has to know
		// who is speaking.
		prompt = fmt.Sprintf("[Pesan dari %s di grup tim]\n%s", speakerName(e.Payload.FromName, e.Payload.From), prompt)
	}

	msg := pending.Message{
		MessageID:  e.Payload.ID,
		ChatID:     e.Payload.ChatID,
		From:       e.Payload.From,
		FromLID:    e.Payload.FromLID,
		Prompt:     prompt,
		ReceivedAt: time.Now(),
		MediaPath:  mediaPath,
		MediaType:  mediaType,
	}

	// Durably record the message BEFORE acking gowa: if the bridge dies
	// between this ack and finishing the reply, gowa won't retry (it
	// already got its 200), but this file survives for main.go to replay
	// on the next startup.
	if err := h.pending.Write(msg); err != nil {
		// Can't guarantee recovery for this one — log it, but still process
		// it now rather than dropping it outright.
		log.Printf("pending: failed to persist message %s: %v", msg.MessageID, err)
	}

	// Ack immediately; gowa retries with backoff if we don't respond within
	// 10s, and Claude can easily take longer than that on a real task.
	w.WriteHeader(http.StatusOK)

	go h.handleMessage(msg)
}

func (h *Handler) handleMessage(msg pending.Message) {
	// One claude -p --resume at a time per chat — see chatlock.go. A second
	// message for the same chat (arrived close together, or replayed
	// alongside this one after a restart) waits its turn instead of racing
	// this one on the same Claude Code session.
	unlock := h.chatLocks.Lock(msg.ChatID)
	defer unlock()

	ctx := context.Background()

	if err := h.gowa.SetChatPresence(msg.ChatID, "start"); err != nil {
		log.Printf("presence: %v", err)
	}

	prevSession, _ := h.store.Get(msg.ChatID)

	prompt := msg.Prompt
	if msg.MediaType == "audio" && msg.MediaPath != "" {
		prompt = h.transcribeAudioPrompt(ctx, msg)
	}

	text, newSession, err := h.runner.Reply(ctx, prompt, prevSession)

	_ = h.gowa.SetChatPresence(msg.ChatID, "stop")

	if err != nil {
		log.Printf("claude: chat=%s from=%s error: %v", msg.ChatID, msg.From, err)
		_ = h.reply(msg, "Maaf, ada error di sisi saya — coba lagi sebentar lagi.")
		h.markDone(msg.MessageID)
		return
	}

	if err := h.store.Set(msg.ChatID, newSession); err != nil {
		log.Printf("session store: %v", err)
	}

	if err := h.reply(msg, text); err != nil {
		log.Printf("send reply: chat=%s error: %v", msg.ChatID, err)
		// Reply never arrived — leave the pending file so a restart retries it.
		return
	}

	log.Printf("claude: chat=%s replied ok (session=%s)", msg.ChatID, newSession)
	h.markDone(msg.MessageID)
}

// transcribeAudioPrompt runs speech-to-text on a voice note and folds the
// result into the final prompt — falls back to the old "can't listen"
// wording if transcription is unavailable (h.transcribe == nil, e.g. missing
// whisper-cli/model) or fails for this specific file.
func (h *Handler) transcribeAudioPrompt(ctx context.Context, msg pending.Message) string {
	if h.transcribe == nil {
		return FinalizeAudioPrompt(msg.Prompt, msg.MediaPath, "", fmt.Errorf("transcription not configured"))
	}
	text, err := h.transcribe.Transcribe(ctx, msg.MediaPath)
	if err != nil {
		log.Printf("transcribe: chat=%s media=%s error: %v", msg.ChatID, msg.MediaPath, err)
	}
	return FinalizeAudioPrompt(msg.Prompt, msg.MediaPath, text, err)
}

// Replay re-runs a message recovered from the pending store at startup —
// same handling as a fresh webhook delivery, just entered from main.go
// instead of ServeHTTP.
func (h *Handler) Replay(msg pending.Message) {
	// The queue may hold a message from a sender who has since been removed
	// from the allowlist; the allowlist is enforced again here, not only when
	// the webhook first arrived.
	if d := h.access.Get().Authorize(access.Incoming{ChatID: msg.ChatID, From: msg.From, FromLID: msg.FromLID}); !d.Allow {
		log.Printf("pending: dropping message %s — sender=%s chat=%s is no longer allowed (%s)", msg.MessageID, msg.From, msg.ChatID, d.Reason)
		h.markDone(msg.MessageID)
		return
	}
	log.Printf("pending: replaying message %s (chat=%s, received %s)", msg.MessageID, msg.ChatID, msg.ReceivedAt.Format(time.RFC3339))
	h.handleMessage(msg)
}

func (h *Handler) markDone(messageID string) {
	if messageID == "" {
		return
	}
	if err := h.pending.Done(messageID); err != nil {
		log.Printf("pending: failed to clear message %s: %v", messageID, err)
	}
}

func (h *Handler) validSignature(header string, body []byte) bool {
	sig := strings.TrimPrefix(header, "sha256=")
	if sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(h.cfg.WebhookSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

// decide applies the access policy to one webhook. The bot's phone number
// comes from the event itself; its LID (what an @mention contains when gowa
// could not rewrite it to a number) is looked up from the group's member list
// the first time an unresolved @number shows up.
func (h *Handler) decide(e *event, hasMedia bool) access.Decision {
	pol := h.access.Get()
	in := access.Incoming{ChatID: e.Payload.ChatID, From: e.Payload.From, FromLID: e.Payload.FromLID, Body: e.Payload.Body, HasMedia: hasMedia}
	bot := access.Bot{Phone: digitsOf(e.DeviceID)}

	d := pol.Decide(in, bot)
	if pol.Mode == access.ModeTeam && d.Reason == "team:not_mentioned" && access.HasUnknownMention(in.Body) {
		if lid := h.botLID(pol.Team.Group, bot.Phone); lid != "" {
			bot.LID = lid
			d = pol.Decide(in, bot)
		}
	}

	if h.cfg.LogGroupMessages && access.IsGroupJID(in.ChatID) {
		log.Printf("group-message: chat=%s from=%s from_lid=%s name=%q bot=%s/%s allow=%v reason=%q body=%q",
			in.ChatID, in.From, in.FromLID, e.Payload.FromName, bot.Phone, bot.LID, d.Allow, d.Reason, truncate(in.Body, 200))
	}
	return d
}

func digitsOf(jid string) string {
	u, _ := access.SplitJID(jid)
	if strings.Trim(u, "0123456789") != "" {
		return ""
	}
	return u
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// speakerName is how a group member is labelled in the prompt. WhatsApp
// display names are chosen by the sender, so they are cleaned before use.
func speakerName(name, from string) string {
	if n := access.CleanName(name); n != "" {
		return strings.NewReplacer("[", "(", "]", ")").Replace(n)
	}
	if d := digitsOf(from); d != "" {
		return "+" + d
	}
	return "anggota grup"
}

// botLID returns the bot's own LID as it appears in group. The answer is
// cached (a found LID for 30 minutes, a miss for one) and the lookup is
// bounded so it can never hold up the webhook ack.
func (h *Handler) botLID(group, botPhone string) string {
	if group == "" || botPhone == "" {
		return ""
	}
	h.lidMu.Lock()
	if e, ok := h.lidCache[group]; ok {
		ttl := time.Minute
		if e.lid != "" {
			ttl = 30 * time.Minute
		}
		if time.Since(e.at) < ttl {
			h.lidMu.Unlock()
			return e.lid
		}
	}
	h.lidMu.Unlock()

	done := make(chan string, 1)
	go func() {
		lid := ""
		info, err := h.gowa.GroupParticipants(group)
		if err != nil {
			log.Printf("group members: %v", err)
		} else {
			for _, p := range info.Participants {
				if n, err := access.NormalizeNumber(p.PhoneNumber); err == nil && n == botPhone {
					lid = access.NormalizeLID(p.LID)
					if lid == "" {
						lid = access.NormalizeLID(p.JID)
					}
					break
				}
			}
		}
		h.lidMu.Lock()
		h.lidCache[group] = lidEntry{lid: lid, at: time.Now()}
		h.lidMu.Unlock()
		done <- lid
	}()
	select {
	case lid := <-done:
		return lid
	case <-time.After(3 * time.Second):
		return ""
	}
}

// reply sends text back to the chat. In a group it quotes the message being
// answered so everyone can see who asked; if gowa refuses the quote (unknown
// message id) the answer is sent without it rather than lost.
func (h *Handler) reply(msg pending.Message, text string) error {
	if access.IsGroupJID(msg.ChatID) && msg.MessageID != "" {
		if err := h.gowa.SendReply(msg.ChatID, text, msg.MessageID); err == nil {
			return nil
		} else {
			log.Printf("send reply with quote failed (%v), retrying without", err)
		}
	}
	return h.gowa.SendMessage(msg.ChatID, text)
}
