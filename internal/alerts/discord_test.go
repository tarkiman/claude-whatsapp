package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDiscord records every payload it receives and can be told to fail.
type fakeDiscord struct {
	mu       sync.Mutex
	payloads []WebhookPayload
	status   int
	server   *httptest.Server
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	t.Helper()
	f := &fakeDiscord{status: http.StatusNoContent}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var p WebhookPayload
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		f.payloads = append(f.payloads, p)
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeDiscord) sent() []WebhookPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]WebhookPayload(nil), f.payloads...)
}

// messages returns each payload's Content line, for tests that only care
// about the short push-notification text.
func (f *fakeDiscord) messages() []string {
	out := make([]string, 0)
	for _, p := range f.sent() {
		out = append(out, p.Content)
	}
	return out
}

func (f *fakeDiscord) fail(code int) {
	f.mu.Lock()
	f.status = code
	f.mu.Unlock()
}

func TestNotifierSendsTheExpectedJSON(t *testing.T) {
	d := newFakeDiscord(t)
	n := NewNotifier()
	payload := WebhookPayload{Content: "hello from the test", Embeds: []Embed{{Title: "T", Description: "D", Color: ColorGreen}}}
	if err := n.Send(context.Background(), d.server.URL, payload); err != nil {
		t.Fatal(err)
	}
	got := d.sent()
	if len(got) != 1 {
		t.Fatalf("payloads = %v", got)
	}
	if got[0].Content != "hello from the test" || len(got[0].Embeds) != 1 || got[0].Embeds[0].Title != "T" {
		t.Fatalf("payload = %+v", got[0])
	}
	if got[0].Username != webhookUsername {
		t.Errorf("username = %q, want a default of %q", got[0].Username, webhookUsername)
	}
}

func TestNotifierRespectsAnExplicitUsername(t *testing.T) {
	d := newFakeDiscord(t)
	n := NewNotifier()
	if err := n.Send(context.Background(), d.server.URL, WebhookPayload{Username: "custom", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if got := d.sent(); len(got) != 1 || got[0].Username != "custom" {
		t.Fatalf("payloads = %v", got)
	}
}

func TestNotifierSurfacesDiscordErrors(t *testing.T) {
	d := newFakeDiscord(t)
	d.fail(http.StatusTooManyRequests)
	n := NewNotifier()
	err := n.Send(context.Background(), d.server.URL, WebhookPayload{Content: "hi"})
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("expected an error mentioning 429, got %v", err)
	}
}

func TestNotifierRejectsAnUnreachableHost(t *testing.T) {
	n := NewNotifier()
	if err := n.Send(context.Background(), "http://127.0.0.1:1/nope", WebhookPayload{Content: "hi"}); err == nil {
		t.Fatal("expected a connection error")
	}
}

func TestTestPayloadIsDistinctFromARealAlert(t *testing.T) {
	p := TestPayload()
	if !strings.Contains(p.Content, "Test") || len(p.Embeds) != 1 {
		t.Fatalf("TestPayload = %+v", p)
	}
	if p.Embeds[0].Color == ColorRed || p.Embeds[0].Color == ColorGreen || p.Embeds[0].Color == ColorYellow {
		t.Error("a test alert should not be colored like a real ok/degraded/down alert")
	}
}
