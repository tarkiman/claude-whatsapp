package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tarkiman/claude-whatsapp/internal/config"
	"github.com/tarkiman/claude-whatsapp/internal/gowa"
)

const (
	testUser = "admin"
	testPass = "correct horse battery"
	lanIP    = "192.168.1.50:5000"
)

// newAuthServer is an admin with no account yet, reachable from loopback and
// from one LAN client, with a cheap password hash and no delay after failures.
func newAuthServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{AccessFile: filepath.Join(dir, "access.json"), AdminAuthFile: filepath.Join(dir, "admin.json"), AlertsFile: filepath.Join(dir, "alerts.json"), AllowedSenders: []string{"6281234567890"}}
	s := New(cfg, gowa.New("http://127.0.0.1:1", "", ""), Options{
		KDFIterations: 1000,
		AllowedNets:   mustNets(t, "192.168.1.0/24"),
		AllowedHosts:  []string{"192.168.1.20"},
	})
	s.failDelay = 0
	return s
}

func req(method, path, body, remote string, cookies ...*http.Cookie) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr, r.Host = remote, "localhost:8098"
	if remote == lanIP {
		r.Host = "192.168.1.20:8098"
	}
	if method == http.MethodPost {
		r.Header.Set(adminHeader, "1")
	}
	for _, c := range cookies {
		if c != nil {
			r.AddCookie(c)
		}
	}
	return r
}

func serve(s *Server, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

func body(user, pass string) string {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	return string(b)
}

// signIn creates the account (from the machine itself) and returns its session.
func signIn(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	s.failDelay = 0
	rec := serve(s, req("POST", "/api/setup", body(testUser, testPass), "127.0.0.1:5000"))
	c := sessionCookie(rec)
	if rec.Code != 200 || c == nil {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body)
	}
	return c
}

func TestEverythingNeedsALogin(t *testing.T) {
	s := newAuthServer(t)
	signIn(t, s)

	for _, p := range []string{"/api/status", "/api/access", "/api/logs", "/api/claude/login", "/api/access/groups", "/api/wa/status?device_id=x"} {
		rec := serve(s, req("GET", p, "", "127.0.0.1:5000"))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session: %d, want 401", p, rec.Code)
		}
	}
	for _, p := range []string{"/api/access", "/api/wa/reconnect", "/api/wa/logout", "/api/claude/login/start", "/api/auth/password"} {
		if rec := serve(s, req("POST", p, "{}", "127.0.0.1:5000")); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without a session: %d, want 401", p, rec.Code)
		}
	}
	for _, p := range []string{"/", "/index.html", "/login.html"} {
		rec := serve(s, req("GET", p, "", "127.0.0.1:5000"))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
			t.Errorf("GET %s without a session: %d -> %q, want a redirect to /login", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := serve(s, req("GET", "/login", "", "127.0.0.1:5000")); rec.Code != 200 || !strings.Contains(rec.Body.String(), "<form") {
		t.Errorf("the login page itself must be public: %d", rec.Code)
	}
}

func TestBasicAuthIsNoLongerAccepted(t *testing.T) {
	s := newAuthServer(t)
	signIn(t, s)
	r := req("GET", "/api/status", "", "127.0.0.1:5000")
	r.SetBasicAuth(testUser, testPass)
	if rec := serve(s, r); rec.Code != http.StatusUnauthorized {
		t.Fatalf("HTTP basic credentials must not open the admin any more: %d", rec.Code)
	}
}

func TestSetupOnlyFromTheMachineItselfAndOnlyOnce(t *testing.T) {
	s := newAuthServer(t)

	state := func(remote string) authStateView {
		var v authStateView
		_ = json.Unmarshal(serve(s, req("GET", "/api/auth/state", "", remote)).Body.Bytes(), &v)
		return v
	}
	if v := state("127.0.0.1:5000"); v.State != "setup" || !v.SetupAllowed {
		t.Fatalf("local client before setup: %+v", v)
	}
	if v := state(lanIP); v.State != "setup" || v.SetupAllowed {
		t.Fatalf("a LAN client must be told to set up on the machine itself: %+v", v)
	}

	if rec := serve(s, req("POST", "/api/setup", body(testUser, testPass), lanIP)); rec.Code != http.StatusForbidden {
		t.Fatalf("setup from the LAN: %d, want 403", rec.Code)
	}
	if exists, _ := s.creds.State(); exists {
		t.Fatal("a LAN client managed to create the account")
	}
	if rec := serve(s, req("POST", "/api/login", body(testUser, testPass), lanIP)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login before any account exists: %d", rec.Code)
	}

	for name, b := range map[string]string{
		"weak password":    body(testUser, "short"),
		"bad username":     body("bad user!", testPass),
		"password == user": body("administrator", "Administrator"),
		"not json":         "{",
	} {
		if rec := serve(s, req("POST", "/api/setup", b, "127.0.0.1:5000")); rec.Code != http.StatusBadRequest {
			t.Errorf("setup with %s: %d, want 400", name, rec.Code)
		}
	}
	if exists, _ := s.creds.State(); exists {
		t.Fatal("an invalid setup created an account")
	}

	rec := serve(s, req("POST", "/api/setup", body(testUser, testPass), "127.0.0.1:5000"))
	if rec.Code != 200 || sessionCookie(rec) == nil {
		t.Fatalf("setup from the machine: %d %s", rec.Code, rec.Body)
	}
	if v := state("127.0.0.1:5000"); v.State != "login" || v.SetupAllowed {
		t.Fatalf("after setup: %+v", v)
	}
	if rec := serve(s, req("POST", "/api/setup", body("someoneelse", "another passphrase"), "127.0.0.1:5000")); rec.Code != http.StatusConflict {
		t.Fatalf("a second setup must be refused: %d", rec.Code)
	}
	if !s.creds.Verify(testUser, testPass) {
		t.Fatal("second setup changed the account")
	}
}

func TestBrokenCredentialsFileIsNotMistakenForFirstRun(t *testing.T) {
	s := newAuthServer(t)
	if err := writeFile(s.creds.Path(), "{ broken"); err != nil {
		t.Fatal(err)
	}
	var v authStateView
	_ = json.Unmarshal(serve(s, req("GET", "/api/auth/state", "", "127.0.0.1:5000")).Body.Bytes(), &v)
	if v.State != "broken" || v.SetupAllowed || v.Error == "" {
		t.Fatalf("state = %+v", v)
	}
	if rec := serve(s, req("POST", "/api/setup", body(testUser, testPass), "127.0.0.1:5000")); rec.Code != http.StatusConflict {
		t.Fatalf("setup over a broken file must be refused (use `bin/admin passwd`): %d", rec.Code)
	}
}

func TestLoginAndTheSessionCookie(t *testing.T) {
	s := newAuthServer(t)
	signIn(t, s)

	rec := serve(s, req("POST", "/api/login", body(testUser, "wrong password!!"), "127.0.0.1:5000"))
	if rec.Code != http.StatusUnauthorized || sessionCookie(rec) != nil {
		t.Fatalf("wrong password: %d, cookie=%v", rec.Code, sessionCookie(rec))
	}
	rec = serve(s, req("POST", "/api/login", body("root", testPass), "127.0.0.1:5000"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong username: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "wrong username or password") || strings.Contains(rec.Body.String(), "root") {
		t.Errorf("the message must not say which part was wrong: %s", rec.Body)
	}

	rec = serve(s, req("POST", "/api/login", body(testUser, testPass), "127.0.0.1:5000"))
	c := sessionCookie(rec)
	if rec.Code != 200 || c == nil {
		t.Fatalf("right credentials: %d %s", rec.Code, rec.Body)
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge <= 0 {
		t.Errorf("cookie attributes: %+v", c)
	}
	if c.Secure {
		t.Error("over plain HTTP the cookie cannot be Secure")
	}
	if strings.Contains(c.Value, testPass) || len(c.Value) < 40 {
		t.Errorf("session token looks weak or leaks the password: %q", c.Value)
	}
	if rec := serve(s, req("GET", "/api/access", "", "127.0.0.1:5000", c)); rec.Code != 200 {
		t.Fatalf("authenticated request: %d", rec.Code)
	}
	if rec := serve(s, req("GET", "/", "", "127.0.0.1:5000", c)); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Who can instruct the bot") {
		t.Fatalf("the admin page after login: %d", rec.Code)
	}

	r := req("POST", "/api/login", body(testUser, testPass), "127.0.0.1:5000")
	r.Header.Set("X-Forwarded-Proto", "https")
	if c2 := sessionCookie(serve(s, r)); c2 == nil || !c2.Secure {
		t.Error("behind a TLS proxy the cookie must be Secure")
	}

	var v authStateView
	_ = json.Unmarshal(serve(s, req("GET", "/api/auth/state", "", "127.0.0.1:5000", c)).Body.Bytes(), &v)
	if !v.Authenticated || v.User != testUser {
		t.Errorf("state for a signed-in browser: %+v", v)
	}
}

func TestLoginIsProtectedAgainstCrossSiteRequests(t *testing.T) {
	s := newAuthServer(t)
	signIn(t, s)
	noHeader := req("POST", "/api/login", body(testUser, testPass), "127.0.0.1:5000")
	noHeader.Header.Del(adminHeader)
	if rec := serve(s, noHeader); rec.Code != http.StatusForbidden {
		t.Errorf("login without %s: %d, want 403", adminHeader, rec.Code)
	}
	foreign := req("POST", "/api/login", body(testUser, testPass), "127.0.0.1:5000")
	foreign.Header.Set("Origin", "http://evil.example")
	if rec := serve(s, foreign); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin login: %d, want 403", rec.Code)
	}
}

func TestTooManyWrongPasswordsLockTheClientOut(t *testing.T) {
	s := newAuthServer(t)
	signIn(t, s)
	for i := 0; i < 5; i++ {
		if rec := serve(s, req("POST", "/api/login", body(testUser, "wrong password!!"), "127.0.0.1:5000")); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, rec.Code)
		}
	}
	rec := serve(s, req("POST", "/api/login", body(testUser, testPass), "127.0.0.1:5000"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("after 5 failures even the right password must wait: %d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := serve(s, req("POST", "/api/login", body(testUser, testPass), lanIP)); rec.Code != 200 {
		t.Fatalf("one client's lockout must not lock out others: %d", rec.Code)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	s := newAuthServer(t)
	c := signIn(t, s)
	rec := serve(s, req("POST", "/api/logout", "{}", "127.0.0.1:5000", c))
	if rec.Code != 200 {
		t.Fatalf("logout: %d", rec.Code)
	}
	if cleared := rec.Result().Cookies(); len(cleared) == 0 || cleared[0].MaxAge >= 0 {
		t.Errorf("the cookie should be cleared: %v", cleared)
	}
	if rec := serve(s, req("GET", "/api/access", "", "127.0.0.1:5000", c)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the old session still works after logout: %d", rec.Code)
	}
}

func changePassword(s *Server, c *http.Cookie, current, next string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"current": current, "new": next})
	return serve(s, req("POST", "/api/auth/password", string(b), "127.0.0.1:5000", c))
}

func TestChangePassword(t *testing.T) {
	s := newAuthServer(t)
	mine := signIn(t, s)
	other := sessionCookie(serve(s, req("POST", "/api/login", body(testUser, testPass), lanIP))) // another browser
	if other == nil {
		t.Fatal("second login failed")
	}

	if rec := changePassword(s, nil, testPass, "a brand new passphrase"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("changing the password without a session: %d", rec.Code)
	}
	if rec := changePassword(s, mine, "not the current one", "a brand new passphrase"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password: %d, want 401", rec.Code)
	}
	if rec := changePassword(s, mine, testPass, "short"); rec.Code != http.StatusBadRequest {
		t.Fatalf("weak new password: %d, want 400", rec.Code)
	}
	if rec := changePassword(s, mine, testPass, testPass); rec.Code != http.StatusBadRequest {
		t.Fatalf("unchanged password: %d, want 400", rec.Code)
	}
	if !s.creds.Verify(testUser, testPass) {
		t.Fatal("a rejected change altered the password")
	}

	rec := changePassword(s, mine, testPass, "a brand new passphrase")
	fresh := sessionCookie(rec)
	if rec.Code != 200 || fresh == nil {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if s.creds.Verify(testUser, testPass) || !s.creds.Verify(testUser, "a brand new passphrase") {
		t.Fatal("the password did not change")
	}
	if rec := serve(s, req("GET", "/api/access", "", "127.0.0.1:5000", fresh)); rec.Code != 200 {
		t.Fatalf("the browser that changed it must stay signed in: %d", rec.Code)
	}
	for name, c := range map[string]*http.Cookie{"the previous session": mine, "another browser": other} {
		if rec := serve(s, req("GET", "/api/access", "", "127.0.0.1:5000", c)); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s survived the password change: %d", name, rec.Code)
		}
	}
	if rec := serve(s, req("POST", "/api/login", body(testUser, testPass), "127.0.0.1:5000")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the old password still logs in: %d", rec.Code)
	}
}

func TestGuessingTheCurrentPasswordIsRateLimitedToo(t *testing.T) {
	s := newAuthServer(t)
	c := signIn(t, s)
	for i := 0; i < 5; i++ {
		changePassword(s, c, "guess number "+strings.Repeat("x", i+1), "a brand new passphrase")
	}
	if rec := changePassword(s, c, testPass, "a brand new passphrase"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the change form must not be a free password oracle: %d", rec.Code)
	}
}

func TestPasswordChangedOutsideTheUISignsEverybodyOut(t *testing.T) {
	// `bin/admin passwd` run on the machine while the admin is up.
	s := newAuthServer(t)
	c := signIn(t, s)
	if rec := serve(s, req("GET", "/api/access", "", "127.0.0.1:5000", c)); rec.Code != 200 {
		t.Fatal("setup session should work")
	}
	if err := s.creds.Set(testUser, "reset from the command line"); err != nil {
		t.Fatal(err)
	}
	if rec := serve(s, req("GET", "/api/access", "", "127.0.0.1:5000", c)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a session outlived a password reset: %d", rec.Code)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
