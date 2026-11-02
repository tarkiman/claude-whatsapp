package adminauth

import (
	"testing"
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/totp"
)

func codeFor(t *testing.T, secret string) string {
	t.Helper()
	return codeAfter(t, secret, 0)
}

// codeAfter returns the code for a time offset from now — used to get a
// code from a different TOTP step than one already consumed earlier in the
// same (fast, sub-30s) test, while staying within ValidateAt's ±1-step skew
// tolerance of the real clock.
func codeAfter(t *testing.T, secret string, d time.Duration) string {
	t.Helper()
	c, err := totp.CodeAt(secret, time.Now().Add(d))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTOTPBeginFailsWithoutAnAccountYet(t *testing.T) {
	s := newStore(t)
	if _, _, err := s.BeginTOTPEnrollment(); err == nil {
		t.Fatal("expected an error enrolling before any account exists")
	}
}

func TestTOTPEnrollmentRequiresConfirmation(t *testing.T) {
	s := newStore(t)
	if err := s.Set("admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if s.TOTPEnabled() {
		t.Fatal("2FA must start off")
	}

	secret, uri, err := s.BeginTOTPEnrollment()
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || uri == "" {
		t.Fatalf("secret/uri = %q/%q, want both non-empty", secret, uri)
	}

	// A correct code against the PENDING secret must not grant anything
	// until it is explicitly confirmed — a half-finished enrollment must
	// never turn into a surprise login requirement.
	if s.TOTPEnabled() {
		t.Fatal("2FA must stay off while enrollment is only pending")
	}
	if s.VerifyTOTPCode(codeFor(t, secret)) {
		t.Fatal("a pending secret's code must not verify a login — it isn't enabled yet")
	}

	if err := s.ConfirmTOTP("000000"); err == nil {
		t.Fatal("expected the obviously wrong confirmation code to be rejected")
	}
	if s.TOTPEnabled() {
		t.Fatal("a wrong confirmation code must not enable 2FA")
	}

	if err := s.ConfirmTOTP(codeFor(t, secret)); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !s.TOTPEnabled() {
		t.Fatal("2FA should be enabled after a correct confirmation")
	}
}

func TestTOTPReplayIsRejected(t *testing.T) {
	s := newStore(t)
	if err := s.Set("admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	secret, _, err := s.BeginTOTPEnrollment()
	if err != nil {
		t.Fatal(err)
	}
	// Confirmation itself consumes a step; a unit test runs far faster than
	// the 30s step length, so get the next test code from one step later —
	// still within ValidateAt's own ±1-step skew tolerance of the real
	// "now" used inside VerifyTOTPCode, but a step the confirm call above
	// did not already consume.
	if err := s.ConfirmTOTP(codeFor(t, secret)); err != nil {
		t.Fatal(err)
	}

	code := codeAfter(t, secret, 30*time.Second)
	if !s.VerifyTOTPCode(code) {
		t.Fatal("the first use of a valid code should verify")
	}
	if s.VerifyTOTPCode(code) {
		t.Fatal("reusing the exact same code must be rejected (replay)")
	}
}

func TestTOTPDisableTurnsItOffAndInvalidatesTheSecret(t *testing.T) {
	s := newStore(t)
	if err := s.Set("admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	secret, _, err := s.BeginTOTPEnrollment()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmTOTP(codeFor(t, secret)); err != nil {
		t.Fatal(err)
	}

	if err := s.DisableTOTP(); err != nil {
		t.Fatal(err)
	}
	if s.TOTPEnabled() {
		t.Fatal("2FA should be off after DisableTOTP")
	}
	if s.VerifyTOTPCode(codeFor(t, secret)) {
		t.Fatal("a code for the old (disabled) secret must never verify again")
	}
}

// TestPasswordChangePreservesTOTPState is the regression this feature was
// built to avoid: Set() (used for password changes too) must carry the
// existing TOTP secret/enabled flag forward, not silently wipe it.
func TestPasswordChangePreservesTOTPState(t *testing.T) {
	s := newStore(t)
	if err := s.Set("admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	secret, _, err := s.BeginTOTPEnrollment()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmTOTP(codeFor(t, secret)); err != nil {
		t.Fatal(err)
	}

	if err := s.Set("admin", "a brand new long enough password"); err != nil {
		t.Fatalf("password change: %v", err)
	}

	if !s.TOTPEnabled() {
		t.Fatal("changing the password must not disable 2FA")
	}
	// One step past the one ConfirmTOTP already consumed above — see the
	// comment in TestTOTPReplayIsRejected.
	if !s.VerifyTOTPCode(codeAfter(t, secret, 30*time.Second)) {
		t.Fatal("the TOTP secret must still work after a password change")
	}
}
