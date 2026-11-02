package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/adminauth"
	"github.com/tarkiman/claude-whatsapp/internal/totp"
)

func runTOTPCmd(t *testing.T, path string, args ...string) (code int, out, errw string) {
	t.Helper()
	var o, e bytes.Buffer
	code = runTOTP(totpEnv{path: path, out: &o, errw: &e}, args)
	return code, o.String(), e.String()
}

func TestTOTPCLIUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.json")
	if code, _, errw := runTOTPCmd(t, path); code != 2 || !strings.Contains(errw, "usage") {
		t.Errorf("no subcommand: code=%d errw=%q", code, errw)
	}
	if code, _, errw := runTOTPCmd(t, path, "bogus"); code != 2 || !strings.Contains(errw, "usage") {
		t.Errorf("bad subcommand: code=%d errw=%q", code, errw)
	}
}

func TestTOTPCLIStatusWithoutAnAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.json")
	if code, _, _ := runTOTPCmd(t, path, "status"); code != 1 {
		t.Errorf("status before any account: code=%d, want 1", code)
	}
}

func TestTOTPCLIDisableWithoutAnAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.json")
	if code, _, errw := runTOTPCmd(t, path, "disable"); code != 1 || !strings.Contains(errw, "no admin account") {
		t.Errorf("disable before any account: code=%d errw=%q", code, errw)
	}
}

func TestTOTPCLIStatusAndDisable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.json")
	store := adminauth.Open(path, 1000)
	if err := store.Set("admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}

	if code, out, _ := runTOTPCmd(t, path, "status"); code != 0 || !strings.Contains(out, "OFF") {
		t.Fatalf("status before enrollment: code=%d out=%q", code, out)
	}
	if code, out, _ := runTOTPCmd(t, path, "disable"); code != 0 || !strings.Contains(out, "already off") {
		t.Fatalf("disable when already off: code=%d out=%q", code, out)
	}

	secret, _, err := store.BeginTOTPEnrollment()
	if err != nil {
		t.Fatal(err)
	}
	code, err2 := totp.CodeAt(secret, time.Now())
	if err2 != nil {
		t.Fatal(err2)
	}
	if err := store.ConfirmTOTP(code); err != nil {
		t.Fatal(err)
	}

	if code, out, _ := runTOTPCmd(t, path, "status"); code != 0 || !strings.Contains(out, "ON") {
		t.Fatalf("status once enabled: code=%d out=%q", code, out)
	}

	// This is the recovery scenario the CLI exists for: a locked-out
	// operator, running it directly on the machine, with no session and no
	// code — it must actually turn 2FA off.
	if exitCode, out, _ := runTOTPCmd(t, path, "disable"); exitCode != 0 || !strings.Contains(out, "disabled") {
		t.Fatalf("disable: code=%d out=%q", exitCode, out)
	}
	if store.TOTPEnabled() {
		t.Fatal("2FA should be off after the CLI disable")
	}
}
