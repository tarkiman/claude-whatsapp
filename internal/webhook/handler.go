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
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/claude"
	"github.com/tarkiman/claude-whatsapp/internal/config"
	"github.com/tarkiman/claude-whatsapp/internal/gowa"
	"github.com/tarkiman/claude-whatsapp/internal/pending"
	"github.com/tarkiman/claude-whatsapp/internal/session"
	"github.com/tarkiman/claude-whatsapp/internal/transcribe"
)

type event struct {
	Event   string  `json:"event"`
	Payload payload `json:"payload"`
}

type payload struct {
	ID       string `json:"id"`
	ChatID   string `json:"chat_id"`
	From     string `json:"from"`
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
}

func New(cfg *config.Config, gowaClient *gowa.Client, runner *claude.Runner, store *session.Store, pendingStore *pending.Store, transcriber *transcribe.Transcriber) *Handler {
	return &Handler{cfg: cfg, gowa: gowaClient, runner: runner, store: store, pending: pendingStore, transcribe: transcriber, chatLocks: newChatLocks()}
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
	if !h.cfg.IsAllowed(e.Payload.ChatID, e.Payload.From) {
		log.Printf("webhook: ignoring message from disallowed sender=%s chat=%s", e.Payload.From, e.Payload.ChatID)
		w.WriteHeader(http.StatusOK)
		return
	}

	mediaPath := resolveMediaPath(h.cfg, ref)
	prompt := buildPrompt(e.Payload.Body, mediaType, mediaPath, ref)

	msg := pending.Message{
		MessageID:  e.Payload.ID,
		ChatID:     e.Payload.ChatID,
		From:       e.Payload.From,
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
		_ = h.gowa.SendMessage(msg.ChatID, "Maaf, ada error di sisi saya — coba lagi sebentar lagi.")
		h.markDone(msg.MessageID)
		return
	}

	if err := h.store.Set(msg.ChatID, newSession); err != nil {
		log.Printf("session store: %v", err)
	}

	if err := h.gowa.SendMessage(msg.ChatID, text); err != nil {
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
