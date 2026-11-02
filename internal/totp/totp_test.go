package totp

import (
	"strings"
	"testing"
	"time"
)

// TestHOTPMatchesRFC4226Vectors checks the unexported hotp() function against
// the official RFC 4226 Appendix D test vectors (ASCII secret
// "12345678901234567890", counters 0–9) — this proves the HMAC-SHA1 +
// dynamic-truncation implementation is conformant, not just internally
// self-consistent.
func TestHOTPMatchesRFC4226Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	want := []string{
		"755224", "287082", "359152", "969429", "338314",
		"254676", "287922", "162583", "399871", "520489",
	}
	for counter, w := range want {
		if got := hotp(secret, uint64(counter)); got != w {
			t.Errorf("hotp(counter=%d) = %q, want %q", counter, got, w)
		}
	}
}

func TestGenerateSecretIsUsableAndRandom(t *testing.T) {
	a, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two calls returned the same secret")
	}
	if strings.ContainsAny(a, "=") {
		t.Errorf("secret %q should have no base32 padding", a)
	}
	if _, ok := ValidateAt(a, "000000", time.Now()); ok {
		t.Error("an arbitrary code should not happen to validate")
	}
}

func TestValidateAtAcceptsTheCurrentCode(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0) // fixed, so the test isn't flaky at a step boundary
	key, _ := b32.DecodeString(secret)
	code := hotp(key, uint64(now.Unix()/stepSecs))

	step, ok := ValidateAt(secret, code, now)
	if !ok {
		t.Fatal("the current step's code was rejected")
	}
	if want := now.Unix() / stepSecs; step != want {
		t.Errorf("returned step = %d, want %d", step, want)
	}
}

func TestValidateAtToleratesOneStepOfClockSkew(t *testing.T) {
	secret, _ := GenerateSecret()
	key, _ := b32.DecodeString(secret)
	now := time.Unix(1_700_000_000, 0)
	curStep := now.Unix() / stepSecs

	oneStepAhead := hotp(key, uint64(curStep)+1)
	if _, ok := ValidateAt(secret, oneStepAhead, now); !ok {
		t.Error("a code from one step ahead (phone clock slightly fast) should still validate")
	}
	oneStepBehind := hotp(key, uint64(curStep)-1)
	if _, ok := ValidateAt(secret, oneStepBehind, now); !ok {
		t.Error("a code from one step behind (phone clock slightly slow) should still validate")
	}

	twoStepsAhead := hotp(key, uint64(curStep)+2)
	if _, ok := ValidateAt(secret, twoStepsAhead, now); ok {
		t.Error("a code two steps away is outside the tolerated skew and must be rejected")
	}
}

func TestValidateAtRejectsGarbage(t *testing.T) {
	secret, _ := GenerateSecret()
	cases := []string{"", "12345", "1234567", "abcdef", " 123456", "123 456"}
	for _, c := range cases {
		if _, ok := ValidateAt(secret, c, time.Now()); ok {
			t.Errorf("code %q should never validate", c)
		}
	}
	if _, ok := ValidateAt("not-valid-base32!!", "123456", time.Now()); ok {
		t.Error("an undecodable secret should never validate")
	}
}

func TestURIContainsTheExpectedParameters(t *testing.T) {
	secret, _ := GenerateSecret()
	uri := URI(secret, "admin", "claude-whatsapp")
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("URI = %q, want an otpauth://totp/ link", uri)
	}
	for _, want := range []string{"secret=" + secret, "issuer=claude-whatsapp", "algorithm=SHA1", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI %q missing %q", uri, want)
		}
	}
}
