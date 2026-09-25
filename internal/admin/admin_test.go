package admin

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		auth    string // "user:pass" for basic auth
		want    int
	}{
		{"loopback GET", Options{}, "GET", "127.0.0.1:5000", "127.0.0.1:8098", nil, "", 200},
		{"localhost GET", Options{}, "GET", "127.0.0.1:5000", "localhost:8098", nil, "", 200},
		{"rebinding host", Options{}, "GET", "127.0.0.1:5000", "evil.example:8098", nil, "", 403},
		{"POST without admin header", Options{}, "POST", "127.0.0.1:5000", "localhost:8098", nil, "", 403},
		{"POST with header", Options{}, "POST", "127.0.0.1:5000", "localhost:8098", admin, "", 200},
		{"POST cross-origin", Options{}, "POST", "127.0.0.1:5000", "localhost:8098", map[string]string{adminHeader: "1", "Origin": "http://evil.example"}, "", 403},
		{"POST same-origin", Options{}, "POST", "127.0.0.1:5000", "localhost:8098", map[string]string{adminHeader: "1", "Origin": "http://localhost:8098"}, "", 200},

		{"LAN client not allowed by default", Options{}, "GET", "192.168.1.50:5000", "localhost:8098", nil, "", 403},
		{"LAN client in allowed net", Options{AllowedNets: mustNets(t, "192.168.1.0/24"), AllowedHosts: []string{"192.168.1.11"}}, "GET", "192.168.1.50:5000", "192.168.1.11:8098", nil, "", 200},
		{"ZeroTier client in allowed net", Options{AllowedNets: mustNets(t, "192.168.1.0/24", "172.22.0.0/16"), AllowedHosts: []string{"172.22.195.49"}}, "GET", "172.22.10.7:5000", "172.22.195.49:8098", nil, "", 200},
		{"outside client refused", Options{AllowedNets: mustNets(t, "192.168.1.0/24", "172.22.0.0/16")}, "GET", "8.8.8.8:5000", "192.168.1.11:8098", nil, "", 403},
		{"allowed client but unlisted Host", Options{AllowedNets: mustNets(t, "192.168.1.0/24")}, "GET", "192.168.1.50:5000", "192.168.1.11:8098", nil, "", 403},

		{"password required", Options{Password: "s3cret"}, "GET", "127.0.0.1:5000", "localhost:8098", nil, "", 401},
		{"wrong password", Options{Password: "s3cret"}, "GET", "127.0.0.1:5000", "localhost:8098", nil, "admin:nope", 401},
		{"right password", Options{Password: "s3cret"}, "GET", "127.0.0.1:5000", "localhost:8098", nil, "admin:s3cret", 200},
	}
	for _, c := range cases {
		h := (&Server{opts: c.opts}).guard(ok)
		req := httptest.NewRequest(c.method, "/x", nil)
		req.RemoteAddr = c.remote
		req.Host = c.host
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		if c.auth != "" {
			u, p, _ := strings.Cut(c.auth, ":")
			req.SetBasicAuth(u, p)
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

	backlog := healthy
	backlog.Bridge.Pending = 2
	if got, _ := verdict(backlog); got != "degraded" {
		t.Errorf("pending backlog: got %q, want degraded", got)
	}
}
