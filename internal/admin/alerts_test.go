package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/tarkiman/claude-whatsapp/internal/alerts"
)

const validHook = "https://discord.com/api/webhooks/123456789012345678/AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abc"

// fakeDiscordServer records every message it receives. A redirectClient lets
// the Notifier keep using a genuine https://discord.com/... URL — so
// internal/alerts' real host validation is exercised unmodified — while the
// actual HTTP request lands on this local server.
type fakeDiscordServer struct {
	mu       sync.Mutex
	payloads []alerts.WebhookPayload
	srv      *httptest.Server
}

func newFakeDiscordServer(t *testing.T) *fakeDiscordServer {
	t.Helper()
	f := &fakeDiscordServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body alerts.WebhookPayload
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.payloads = append(f.payloads, body)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDiscordServer) sent() []alerts.WebhookPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]alerts.WebhookPayload(nil), f.payloads...)
}

// messages returns each payload's Content line, for tests that only care
// about the short push-notification text.
func (f *fakeDiscordServer) messages() []string {
	out := make([]string, 0)
	for _, p := range f.sent() {
		out = append(out, p.Content)
	}
	return out
}

// redirectClient returns an *http.Client that sends every request to target
// regardless of the URL's own scheme/host, keeping the path/query intact.
func (f *fakeDiscordServer) redirectClient() *http.Client {
	target, _ := url.Parse(f.srv.URL)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		r.Host = target.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAlertsSaveValidatesAndMasksTheWebhook(t *testing.T) {
	a := newAccessRig(t)

	code, body := a.do(http.MethodGet, "/api/alerts", "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var v alertsView
	_ = json.Unmarshal(body, &v)
	if v.Enabled || v.Configured {
		t.Fatalf("fresh install should have no alerts configured: %+v", v)
	}

	if code, _ := a.do(http.MethodPost, "/api/alerts", `{"enabled":true,"webhookUrl":"not a webhook"}`); code != http.StatusBadRequest {
		t.Fatalf("invalid webhook while enabling: %d, want 400", code)
	}
	if code, _ := a.do(http.MethodPost, "/api/alerts", `{"enabled":true}`); code != http.StatusBadRequest {
		t.Fatalf("enabling with no webhook ever saved: %d, want 400", code)
	}

	code, body = a.do(http.MethodPost, "/api/alerts", `{"enabled":true,"webhookUrl":"`+validHook+`"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	_ = json.Unmarshal(body, &v)
	if !v.Enabled || !v.Configured {
		t.Fatalf("after saving: %+v", v)
	}
	if strings.Contains(v.WebhookMask, "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abc") {
		t.Fatalf("the API response must never include the raw webhook: %+v", v)
	}

	// Disabling without repeating the URL must keep the one already stored.
	if code, _ := a.do(http.MethodPost, "/api/alerts", `{"enabled":false}`); code != 200 {
		t.Fatal("disabling should not require the webhook again")
	}
	code, body = a.do(http.MethodGet, "/api/alerts", "")
	_ = json.Unmarshal(body, &v)
	if v.Enabled || !v.Configured {
		t.Fatalf("after disabling, the webhook should still be remembered: %+v", v)
	}
}

func TestAlertsEndpointsRequireLogin(t *testing.T) {
	s := newAuthServer(t)
	signIn(t, s)
	if rec := serve(s, req("GET", "/api/alerts", "", "127.0.0.1:5000")); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/alerts without a session: %d", rec.Code)
	}
	for _, p := range []string{"/api/alerts", "/api/alerts/test"} {
		if rec := serve(s, req("POST", p, "{}", "127.0.0.1:5000")); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without a session: %d", p, rec.Code)
		}
	}
}

func TestAlertsTestEndpointSendsARealMessage(t *testing.T) {
	a := newAccessRig(t)
	if code, _ := a.do(http.MethodPost, "/api/alerts/test", ""); code != http.StatusBadRequest {
		t.Fatalf("test with nothing saved yet: %d, want 400", code)
	}

	d := newFakeDiscordServer(t)
	a.s.notifier = &alerts.Notifier{HTTP: d.redirectClient()}
	// Saved disabled on purpose: the test-send button must work regardless of
	// whether alerting is turned on.
	if code, body := a.do(http.MethodPost, "/api/alerts", `{"enabled":false,"webhookUrl":"`+validHook+`"}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}

	if code, body := a.do(http.MethodPost, "/api/alerts/test", ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	got := d.messages()
	if len(got) != 1 || !strings.Contains(got[0], "Test alert") {
		t.Fatalf("messages = %v", got)
	}
}

func TestAlertsTestReportsADeadWebhook(t *testing.T) {
	a := newAccessRig(t)
	if code, _ := a.do(http.MethodPost, "/api/alerts", `{"enabled":false,"webhookUrl":"`+validHook+`"}`); code != 200 {
		t.Fatal("setup save failed")
	}
	a.s.notifier = &alerts.Notifier{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}}
	if code, body := a.do(http.MethodPost, "/api/alerts/test", ""); code != http.StatusBadGateway {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAlertCycleFeedsRealStatusIntoTheMonitor(t *testing.T) {
	// End-to-end: an unhealthy stack (a freshly-created server has no
	// WhatsApp device) must produce a real Discord POST on the very first
	// cycle, using the SAME status computation the dashboard shows — through
	// the real save/validate path, not a hand-written file.
	a := newAccessRig(t)
	d := newFakeDiscordServer(t)
	a.s.notifier = &alerts.Notifier{HTTP: d.redirectClient()}
	a.s.alertMon = alerts.NewMonitor(a.s.notifier)
	if code, body := a.do(http.MethodPost, "/api/alerts", `{"enabled":true,"webhookUrl":"`+validHook+`"}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}

	a.s.alertCycle(context.Background())
	got := d.sent()
	if len(got) != 1 {
		t.Fatalf("payloads = %v, want exactly 1", got)
	}
	if !strings.Contains(got[0].Content, "down") && !strings.Contains(got[0].Content, "degraded") {
		t.Errorf("a freshly-created server with no WhatsApp device should not read as healthy: %q", got[0].Content)
	}
	if len(got[0].Embeds) != 1 {
		t.Fatalf("expected one embed, got %+v", got[0])
	}
	names := map[string]bool{}
	for _, f := range got[0].Embeds[0].Fields {
		names[f.Name] = true
	}
	for _, want := range []string{"🌉 Bridge", "📱 WhatsApp", "🤖 Claude"} {
		if !names[want] {
			t.Errorf("embed is missing the %s component field: %+v", want, got[0].Embeds[0].Fields)
		}
	}

	// A second cycle with nothing changed must not repeat the alert.
	a.s.alertCycle(context.Background())
	if got := d.sent(); len(got) != 1 {
		t.Fatalf("second cycle sent again: %v", got)
	}
}
