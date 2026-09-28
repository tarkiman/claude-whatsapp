package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/claude"
	"github.com/tarkiman/claude-whatsapp/internal/config"
	"github.com/tarkiman/claude-whatsapp/internal/gowa"
	"github.com/tarkiman/claude-whatsapp/internal/pending"
	"github.com/tarkiman/claude-whatsapp/internal/session"
)

const testSecret = "test-webhook-secret"

// rig is a real Handler wired to a fake `claude` that records every prompt it
// is asked to run, and a fake gowa. The question each test asks is simply:
// did a given webhook make Claude run?
type rig struct {
	t       *testing.T
	h       *Handler
	pending *pending.Store
	marker  string
	seq     int
}

func newRig(t *testing.T, allowedSenders, allowedGroups []string) *rig {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "claude-calls.log")

	// $2 is the prompt: claude is invoked as `claude -p <prompt> ...`.
	script := "#!/bin/sh\necho \"$2\" >> " + marker + "\n" +
		`echo '{"type":"result","is_error":false,"result":"ok","session_id":"sess-1"}'` + "\n"
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"SUCCESS","message":"ok"}`))
	}))
	t.Cleanup(gw.Close)

	store, err := session.Open(filepath.Join(dir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	pend, err := pending.Open(filepath.Join(dir, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		WebhookSecret:  testSecret,
		AllowedSenders: allowedSenders,
		AllowedGroups:  allowedGroups,
		MediaDir:       dir,
	}
	h := New(cfg, gowa.New(gw.URL, "", ""), claude.New(bin, dir), store, pend, nil)
	return &rig{t: t, h: h, pending: pend, marker: marker}
}

type msg struct {
	chatID, from, body string
	fromMe             bool
}

func (r *rig) body(m msg) []byte {
	r.seq++
	b, _ := json.Marshal(map[string]any{
		"event": "message",
		"payload": map[string]any{
			"id": "msg-" + string(rune('a'+r.seq)), "chat_id": m.chatID, "from": m.from,
			"is_from_me": m.fromMe, "body": m.body,
		},
	})
	return b
}

func sign(b []byte) string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write(b)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (r *rig) post(b []byte, sig string) int {
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(b)))
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec.Code
}

// send delivers a correctly signed webhook.
func (r *rig) send(m msg) int {
	b := r.body(m)
	return r.post(b, sign(b))
}

// prompts waits long enough for any (wrongly) spawned async work to have
// happened, then returns the prompts Claude was actually asked to run.
func (r *rig) prompts() []string {
	time.Sleep(400 * time.Millisecond)
	raw, err := os.ReadFile(r.marker)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func (r *rig) wantClaudeRan(name string, m msg) {
	r.t.Helper()
	if code := r.send(m); code != http.StatusOK {
		r.t.Fatalf("%s: HTTP %d, want 200", name, code)
	}
	if got := r.prompts(); len(got) == 0 {
		r.t.Fatalf("%s: claude did NOT run, but should have", name)
	}
}

func (r *rig) wantClaudeIgnored(name string, m msg) {
	r.t.Helper()
	code := r.send(m)
	if code != http.StatusOK {
		r.t.Fatalf("%s: HTTP %d, want 200 (silently ignored)", name, code)
	}
	if got := r.prompts(); len(got) != 0 {
		r.t.Fatalf("%s: SECURITY — claude ran for a message that must be ignored: %q", name, got)
	}
}

const (
	owner   = "6281234567890@s.whatsapp.net"
	dmChat  = owner
	groupOK = "120363012345678901@g.us"
)

func TestAllowedSenderReachesClaude(t *testing.T) {
	r := newRig(t, []string{owner}, nil)
	r.wantClaudeRan("allowed sender", msg{chatID: dmChat, from: owner, body: "hello"})
}

func TestUnknownSenderNeverReachesClaude(t *testing.T) {
	r := newRig(t, []string{owner}, nil)
	stranger := "6289999999999@s.whatsapp.net"
	r.wantClaudeIgnored("stranger DM", msg{chatID: stranger, from: stranger, body: "rm -rf ~"})
}

func TestBadOrMissingSignatureIsRejected(t *testing.T) {
	r := newRig(t, []string{owner}, nil)
	b := r.body(msg{chatID: dmChat, from: owner, body: "hello"})

	if code := r.post(b, "sha256=deadbeef"); code != http.StatusUnauthorized {
		t.Errorf("wrong signature: HTTP %d, want 401", code)
	}
	if code := r.post(b, ""); code != http.StatusUnauthorized {
		t.Errorf("missing signature: HTTP %d, want 401", code)
	}
	tampered := append([]byte(nil), b...)
	sig := sign(b)
	tampered[len(tampered)-3] ^= 1 // signed body != delivered body
	if code := r.post(tampered, sig); code != http.StatusUnauthorized && code != http.StatusBadRequest {
		t.Errorf("tampered body: HTTP %d, want 401/400", code)
	}
	if got := r.prompts(); len(got) != 0 {
		t.Fatalf("SECURITY — claude ran for an unauthenticated webhook: %q", got)
	}
}

func TestOwnOutgoingMessagesAreIgnored(t *testing.T) {
	r := newRig(t, []string{owner}, nil)
	r.wantClaudeIgnored("is_from_me", msg{chatID: dmChat, from: owner, body: "echo of our own reply", fromMe: true})
}

func TestGroupsAreOffUnlessListed(t *testing.T) {
	// Even the owner writing in a group that is not allowlisted is ignored.
	r := newRig(t, []string{owner}, nil)
	r.wantClaudeIgnored("owner in unlisted group", msg{chatID: groupOK, from: owner, body: "hi"})
}

func TestListedGroupAllowsAnyMember(t *testing.T) {
	// Documented behaviour (ARCHITECTURE §10): group access is per group, not
	// per member. Pinned here so a change to it is a conscious decision.
	r := newRig(t, []string{owner}, []string{groupOK})
	member := "6289999999999@s.whatsapp.net"
	r.wantClaudeRan("stranger inside listed group", msg{chatID: groupOK, from: member, body: "hi"})
}

// A shorter allowlist entry must not authorise a longer number that merely
// starts with the same digits.
func TestBareNumberIsNotAPrefixWildcard(t *testing.T) {
	r := newRig(t, []string{"628123456789"}, nil)
	other := "6281234567890@s.whatsapp.net" // a different person, same leading digits
	r.wantClaudeIgnored("number sharing a prefix", msg{chatID: other, from: other, body: "hi"})
}

func TestBareNumberStillMatchesItsOwnJID(t *testing.T) {
	r := newRig(t, []string{"6281234567890"}, nil)
	r.wantClaudeRan("bare entry, exact number", msg{chatID: dmChat, from: owner, body: "hi"})
}

func TestOwnersOtherDeviceIsStillTheOwner(t *testing.T) {
	r := newRig(t, []string{owner}, nil)
	device := "6281234567890:12@s.whatsapp.net" // multi-device JID of the same number
	r.wantClaudeRan("owner's second device", msg{chatID: dmChat, from: device, body: "hi"})
}

func TestLIDIsNotMatchedByPhoneNumber(t *testing.T) {
	r := newRig(t, []string{"6281234567890"}, nil)
	lid := "6281234567890@lid"
	r.wantClaudeIgnored("@lid identifier", msg{chatID: lid, from: lid, body: "hi"})
}

// A message queued before its sender was removed from the allowlist must not
// be executed when the bridge restarts and replays the queue.
func TestReplayRechecksTheAllowlist(t *testing.T) {
	r := newRig(t, []string{owner}, nil)
	stranger := "6289999999999@s.whatsapp.net"
	stale := pending.Message{MessageID: "stale-1", ChatID: stranger, From: stranger, Prompt: "rm -rf ~", ReceivedAt: time.Now()}
	if err := r.pending.Write(stale); err != nil {
		t.Fatal(err)
	}
	r.h.Replay(stale)
	if got := r.prompts(); len(got) != 0 {
		t.Fatalf("SECURITY — replay executed a message from a now-disallowed sender: %q", got)
	}
	left, _ := r.pending.ListAll()
	if len(left) != 0 {
		t.Errorf("dropped message should be cleared from the queue, %d left", len(left))
	}
}
