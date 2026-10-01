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

// fakeDiscord records every request it receives and can be told to fail.
type fakeDiscord struct {
	mu     sync.Mutex
	bodies []string
	status int
	server *httptest.Server
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
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body["content"])
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeDiscord) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

func (f *fakeDiscord) fail(code int) {
	f.mu.Lock()
	f.status = code
	f.mu.Unlock()
}

func TestNotifierSendsJSONContent(t *testing.T) {
	d := newFakeDiscord(t)
	n := NewNotifier()
	if err := n.Send(context.Background(), d.server.URL, "hello from the test"); err != nil {
		t.Fatal(err)
	}
	got := d.messages()
	if len(got) != 1 || got[0] != "hello from the test" {
		t.Fatalf("messages = %v", got)
	}
}

func TestNotifierSurfacesDiscordErrors(t *testing.T) {
	d := newFakeDiscord(t)
	d.fail(http.StatusTooManyRequests)
	n := NewNotifier()
	err := n.Send(context.Background(), d.server.URL, "hi")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("expected an error mentioning 429, got %v", err)
	}
}

func TestNotifierRejectsAnUnreachableHost(t *testing.T) {
	n := NewNotifier()
	if err := n.Send(context.Background(), "http://127.0.0.1:1/nope", "hi"); err == nil {
		t.Fatal("expected a connection error")
	}
}
