package admin

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/config"
	"github.com/tarkiman/claude-whatsapp/internal/pending"
)

func mustNets(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func TestGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	admin := map[string]string{adminHeader: "1"}

	cases := []struct {
		name    string
		opts    Options
		method  string
		remote  string
		host    string
		headers map[string]string
		want    int
	}{
		{"loopback GET", Options{}, "GET", "127.0.0.1:5000", "127.0.0.1:8098", nil, 200},
		{"localhost GET", Options{}, "GET", "127.0.0.1:5000", "localhost:8098", nil, 200},
		{"rebinding host", Options{}, "GET", "127.0.0.1:5000", "evil.example:8098", nil, 403},
		{"POST without admin header", Options{}, "POST", "127.0.0.1:5000", "localhost:8098", nil, 403},
		{"POST with header", Options{}, "POST", "127.0.0.1:5000", "localhost:8098", admin, 200},
		{"POST cross-origin", Options{}, "POST", "127.0.0.1:5000", "localhost:8098", map[string]string{adminHeader: "1", "Origin": "http://evil.example"}, 403},
		{"POST same-origin", Options{}, "POST", "127.0.0.1:5000", "localhost:8098", map[string]string{adminHeader: "1", "Origin": "http://localhost:8098"}, 200},

		{"LAN client not allowed by default", Options{}, "GET", "192.168.1.50:5000", "localhost:8098", nil, 403},
		{"LAN client in allowed net", Options{AllowedNets: mustNets(t, "192.168.1.0/24"), AllowedHosts: []string{"192.168.1.20"}}, "GET", "192.168.1.50:5000", "192.168.1.20:8098", nil, 200},
		{"ZeroTier client in allowed net", Options{AllowedNets: mustNets(t, "192.168.1.0/24", "10.147.0.0/16"), AllowedHosts: []string{"10.147.20.15"}}, "GET", "10.147.20.7:5000", "10.147.20.15:8098", nil, 200},
		{"outside client refused", Options{AllowedNets: mustNets(t, "192.168.1.0/24", "10.147.0.0/16")}, "GET", "8.8.8.8:5000", "192.168.1.20:8098", nil, 403},
		{"allowed client but unlisted Host", Options{AllowedNets: mustNets(t, "192.168.1.0/24")}, "GET", "192.168.1.50:5000", "192.168.1.20:8098", nil, 403},
	}
	for _, c := range cases {
		h := (&Server{opts: c.opts}).guard(ok)
		req := httptest.NewRequest(c.method, "/x", nil)
		req.RemoteAddr = c.remote
		req.Host = c.host
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestVerdict(t *testing.T) {
	healthy := StatusResponse{
		Bridge: BridgeInfo{Active: "active", HealthOK: true},
		WA:     WAInfo{Reachable: true, Connected: true, LoggedIn: true, Containers: []ContainerInfo{{Name: "gowa", Up: true}}},
		Claude: ClaudeInfo{LoggedIn: true},
	}
	if got, _ := verdict(healthy); got != "ok" {
		t.Errorf("healthy: got %q, want ok", got)
	}

	loggedOut := healthy
	loggedOut.WA.LoggedIn, loggedOut.WA.Connected = false, false
	if got, _ := verdict(loggedOut); got != "down" {
		t.Errorf("logged out: got %q, want down", got)
	}

	disconnected := healthy
	disconnected.WA.Connected = false
	if got, _ := verdict(disconnected); got != "degraded" {
		t.Errorf("disconnected: got %q, want degraded", got)
	}

	noClaude := healthy
	noClaude.Claude.LoggedIn = false
	if got, _ := verdict(noClaude); got != "down" {
		t.Errorf("claude logged out: got %q, want down", got)
	}

	// A message that's only briefly in the durability queue is normal —
	// claude -p can legitimately take several minutes — so it must not flip
	// the dashboard to degraded or fire a Discord alert.
	freshPending := healthy
	freshPending.Bridge.Pending = 1
	freshPending.Bridge.pendingOldestAge = 2 * time.Minute
	if got, _ := verdict(freshPending); got != "ok" {
		t.Errorf("fresh pending message: got %q, want ok", got)
	}

	// Only once it's been stuck well past any legitimate reply time is it a
	// real problem.
	stuckPending := healthy
	stuckPending.Bridge.Pending = 2
	stuckPending.Bridge.pendingOldestAge = 30 * time.Minute
	if got, _ := verdict(stuckPending); got != "degraded" {
		t.Errorf("stuck pending backlog: got %q, want degraded", got)
	}
}

// TestBridgeInfoTracksOldestPendingAge confirms bridgeInfo() reads the real
// age of the oldest file in the pending queue (via internal/pending), not
// just a raw file count — that age is what verdict() uses to tell "a message
// claude is still working on" apart from "something is actually stuck".
func TestBridgeInfoTracksOldestPendingAge(t *testing.T) {
	dir := t.TempDir()
	pst, err := pending.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := pending.Message{MessageID: "old", ChatID: "c", From: "f", ReceivedAt: time.Now().Add(-30 * time.Minute)}
	fresh := pending.Message{MessageID: "fresh", ChatID: "c", From: "f", ReceivedAt: time.Now().Add(-2 * time.Minute)}
	if err := pst.Write(old); err != nil {
		t.Fatal(err)
	}
	if err := pst.Write(fresh); err != nil {
		t.Fatal(err)
	}

	s := &Server{cfg: &config.Config{PendingDir: dir}}
	b := s.bridgeInfo(context.Background())
	if b.Pending != 2 {
		t.Fatalf("Pending = %d, want 2", b.Pending)
	}
	if b.pendingOldestAge < 29*time.Minute || b.pendingOldestAge > 31*time.Minute {
		t.Fatalf("pendingOldestAge = %v, want ~30m", b.pendingOldestAge)
	}
	if b.PendingOldestFor != "30m" {
		t.Errorf("PendingOldestFor = %q, want 30m", b.PendingOldestFor)
	}
}
