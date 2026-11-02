package admin

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/totp"
)

func loginBody(user, pass, code string) string {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass, "totpCode": code})
	return string(b)
}

func codeField(name, value string) string {
	b, _ := json.Marshal(map[string]string{name: value})
	return string(b)
}

// enrollTOTP drives the full setup -> confirm flow over HTTP and returns the
// enrolled secret, so tests can generate further codes with it.
func enrollTOTP(t *testing.T, s *Server, c *http.Cookie) string {
	t.Helper()
	rec := serve(s, req("POST", "/api/auth/totp/setup", "", "127.0.0.1:5000", c))
	if rec.Code != 200 {
		t.Fatalf("totp/setup: %d %s", rec.Code, rec.Body)
	}
	var out struct{ Secret, URI string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Secret == "" {
		t.Fatalf("totp/setup response: %s (%v)", rec.Body, err)
	}
	code, err := totp.CodeAt(out.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec = serve(s, req("POST", "/api/auth/totp/confirm", codeField("code", code), "127.0.0.1:5000", c))
	if rec.Code != 200 {
		t.Fatalf("totp/confirm: %d %s", rec.Code, rec.Body)
	}
	return out.Secret
}

func TestTOTPEndpointsRequireALogin(t *testing.T) {
	s := newAuthServer(t)
	signIn(t, s)
	for _, p := range []string{"/api/auth/totp/setup", "/api/auth/totp/confirm", "/api/auth/totp/disable"} {
		if rec := serve(s, req("POST", p, "{}", "127.0.0.1:5000")); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without a session: %d, want 401", p, rec.Code)
		}
	}
}

func TestTOTPEnrollmentOverHTTP(t *testing.T) {
	s := newAuthServer(t)
	c := signIn(t, s)

	var v authStateView
	_ = json.Unmarshal(serve(s, req("GET", "/api/auth/state", "", "127.0.0.1:5000", c)).Body.Bytes(), &v)
	if v.TOTPEnabled {
		t.Fatal("2FA must start off")
	}

	secret := enrollTOTP(t, s, c)
	if secret == "" {
		t.Fatal("enrollment did not return a secret")
	}

	_ = json.Unmarshal(serve(s, req("GET", "/api/auth/state", "", "127.0.0.1:5000", c)).Body.Bytes(), &v)
	if !v.TOTPEnabled {
		t.Fatal("2FA should read as enabled after confirming")
	}
}

func TestTOTPConfirmRejectsAWrongCode(t *testing.T) {
	s := newAuthServer(t)
	c := signIn(t, s)
	rec := serve(s, req("POST", "/api/auth/totp/setup", "", "127.0.0.1:5000", c))
	if rec.Code != 200 {
		t.Fatalf("setup: %d", rec.Code)
	}
	rec = serve(s, req("POST", "/api/auth/totp/confirm", codeField("code", "000000"), "127.0.0.1:5000", c))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong confirmation code: %d, want 400", rec.Code)
	}
	var v authStateView
	_ = json.Unmarshal(serve(s, req("GET", "/api/auth/state", "", "127.0.0.1:5000", c)).Body.Bytes(), &v)
	if v.TOTPEnabled {
		t.Fatal("a wrong code must not enable 2FA")
	}
}

func TestLoginRequiresTheCodeOnceEnabled(t *testing.T) {
	s := newAuthServer(t)
	c := signIn(t, s)
	secret := enrollTOTP(t, s, c)

	// Password alone: no session yet, just a "need the code" signal — this
	// must not count as a failed attempt.
	rec := serve(s, req("POST", "/api/login", loginBody(testUser, testPass, ""), "127.0.0.1:5000"))
	var out struct {
		TOTPRequired bool `json:"totpRequired"`
	}
	if rec.Code != 200 || sessionCookie(rec) != nil {
		t.Fatalf("password-only login: %d, cookie=%v", rec.Code, sessionCookie(rec))
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.TOTPRequired {
		t.Fatalf("response should flag totpRequired: %s", rec.Body)
	}

	// Wrong code: rejected, and this DOES count against the limiter.
	rec = serve(s, req("POST", "/api/login", loginBody(testUser, testPass, "000000"), "127.0.0.1:5000"))
	if rec.Code != http.StatusUnauthorized || sessionCookie(rec) != nil {
		t.Fatalf("wrong code: %d", rec.Code)
	}

	// Right password + right code: a real session.
	code, err := totp.CodeAt(secret, time.Now().Add(30*time.Second)) // past the step confirm/setup already used
	if err != nil {
		t.Fatal(err)
	}
	rec = serve(s, req("POST", "/api/login", loginBody(testUser, testPass, code), "127.0.0.1:5000"))
	fresh := sessionCookie(rec)
	if rec.Code != 200 || fresh == nil {
		t.Fatalf("password+code login: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(s, req("GET", "/api/access", "", "127.0.0.1:5000", fresh)); rec.Code != 200 {
		t.Fatalf("the resulting session should actually work: %d", rec.Code)
	}
}

func TestTOTPDisableNeedsTheCurrentPassword(t *testing.T) {
	s := newAuthServer(t)
	c := signIn(t, s)
	enrollTOTP(t, s, c)

	if rec := serve(s, req("POST", "/api/auth/totp/disable", codeField("password", "not the password"), "127.0.0.1:5000", c)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d, want 401", rec.Code)
	}
	var v1 authStateView
	_ = json.Unmarshal(serve(s, req("GET", "/api/auth/state", "", "127.0.0.1:5000", c)).Body.Bytes(), &v1)
	if !v1.TOTPEnabled {
		t.Fatal("a rejected disable must not turn 2FA off")
	}

	if rec := serve(s, req("POST", "/api/auth/totp/disable", codeField("password", testPass), "127.0.0.1:5000", c)); rec.Code != 200 {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	// omitempty means a false TOTPEnabled is simply absent from the JSON —
	// unmarshal into a FRESH struct, not v1, which Unmarshal would
	// otherwise leave at its previous true value for the missing field.
	var v2 authStateView
	_ = json.Unmarshal(serve(s, req("GET", "/api/auth/state", "", "127.0.0.1:5000", c)).Body.Bytes(), &v2)
	if v2.TOTPEnabled {
		t.Fatal("2FA should read as off after disabling")
	}

	// Login no longer asks for a code.
	rec := serve(s, req("POST", "/api/login", loginBody(testUser, testPass, ""), "127.0.0.1:5000"))
	if rec.Code != 200 || sessionCookie(rec) == nil {
		t.Fatalf("login after disabling 2FA: %d %s", rec.Code, rec.Body)
	}
}
