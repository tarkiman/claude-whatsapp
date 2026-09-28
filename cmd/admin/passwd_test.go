package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tarkiman/claude-whatsapp/internal/adminauth"
)

type harness struct {
	t         *testing.T
	path      string
	out, errw bytes.Buffer
}

func (h *harness) run(stdin string, tty bool, secrets []string, args ...string) int {
	h.out.Reset()
	h.errw.Reset()
	i := 0
	return runPasswd(passwdEnv{
		path: h.path, iterations: 1000, in: strings.NewReader(stdin), out: &h.out, errw: &h.errw, tty: tty,
		secret: func(string) (string, error) {
			if i >= len(secrets) {
				return "", errors.New("unexpected extra prompt")
			}
			i++
			return secrets[i-1], nil
		},
	}, args)
}

func newHarness(t *testing.T) *harness {
	return &harness{t: t, path: filepath.Join(t.TempDir(), "state", "admin.json")}
}

func (h *harness) verify(user, pass string) bool {
	return adminauth.Open(h.path, 1000).Verify(user, pass)
}

func TestPasswdFromStdinCreatesTheLogin(t *testing.T) {
	h := newHarness(t)
	if code := h.run("a long enough passphrase\n", false, nil, "--stdin"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errw.String())
	}
	if !h.verify("admin", "a long enough passphrase") {
		t.Fatal("the password was not stored (default username should be admin)")
	}
	if strings.Contains(h.out.String()+h.errw.String(), "a long enough passphrase") {
		t.Fatal("the password was echoed back")
	}
	if !strings.Contains(h.out.String(), `"admin"`) {
		t.Errorf("confirmation should name the user: %q", h.out.String())
	}
	if fi, err := os.Stat(h.path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials file: %v %v", fi, err)
	}
}

func TestPasswdKeepsTheUsernameAndCanRenameIt(t *testing.T) {
	h := newHarness(t)
	h.run("first passphrase here\n", false, nil, "--stdin", "--user", "tarkiman")
	if code := h.run("second passphrase here\n", false, nil, "--stdin"); code != 0 {
		t.Fatal(h.errw.String())
	}
	if !h.verify("tarkiman", "second passphrase here") || h.verify("admin", "second passphrase here") {
		t.Fatal("re-running without --user must keep the existing username")
	}
	h.run("third passphrase here\n", false, nil, "--stdin", "--user", "newname")
	if !h.verify("newname", "third passphrase here") || h.verify("tarkiman", "third passphrase here") {
		t.Fatal("--user should rename the account")
	}
}

func TestPasswdRefusesWeakInputAndLeavesTheOldLoginAlone(t *testing.T) {
	h := newHarness(t)
	h.run("original passphrase here\n", false, nil, "--stdin")
	for name, tc := range map[string]struct {
		stdin string
		args  []string
	}{
		"too short":    {"short\n", []string{"--stdin"}},
		"empty stdin":  {"", []string{"--stdin"}},
		"bad username": {"another good passphrase\n", []string{"--stdin", "--user", "bad user!"}},
		"same as user": {"administrator\n", []string{"--stdin", "--user", "administrator"}},
	} {
		if code := h.run(tc.stdin, false, nil, tc.args...); code == 0 {
			t.Errorf("%s: expected a failure", name)
		}
	}
	if !h.verify("admin", "original passphrase here") {
		t.Fatal("a rejected attempt changed the stored login")
	}
}

func TestPasswdNeedsATerminalOrStdin(t *testing.T) {
	h := newHarness(t)
	if code := h.run("", false, nil); code == 0 || !strings.Contains(h.errw.String(), "--stdin") {
		t.Fatalf("no terminal and no --stdin must fail with a hint: exit %d %q", code, h.errw.String())
	}
	if exists, _ := adminauth.Open(h.path, 1000).State(); exists {
		t.Fatal("nothing should have been written")
	}
}

func TestPasswdInteractive(t *testing.T) {
	h := newHarness(t)
	// typed username (blank = default), then mismatch, then too short, then good.
	code := h.run("\n", true, []string{"good passphrase 1", "different one", "short", "short", "good passphrase 1", "good passphrase 1"})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, h.errw.String())
	}
	if !h.verify("admin", "good passphrase 1") {
		t.Fatal("interactive setup did not store the password")
	}
	if !strings.Contains(h.errw.String(), "differ") || !strings.Contains(h.errw.String(), "at least") {
		t.Errorf("the user should be told why attempts failed: %q", h.errw.String())
	}

	h2 := newHarness(t)
	if code := h2.run("me_admin\n", true, []string{"pw", "pw", "pw", "pw", "pw", "pw"}); code == 0 {
		t.Fatal("three bad attempts in a row must give up")
	}
	if exists, _ := adminauth.Open(h2.path, 1000).State(); exists {
		t.Fatal("a failed interactive run wrote a login")
	}
}

func TestPasswdReplacesABrokenFile(t *testing.T) {
	h := newHarness(t)
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.path, []byte("{ broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("recovered passphrase\n", false, nil, "--stdin"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errw.String())
	}
	if !h.verify("admin", "recovered passphrase") || !strings.Contains(h.errw.String(), "replaced") {
		t.Fatalf("broken file not repaired (stderr: %q)", h.errw.String())
	}
}

func TestCredentialsPathHonoursTheEnvironment(t *testing.T) {
	t.Setenv("ADMIN_AUTH_FILE", "/tmp/custom-admin.json")
	if got := credentialsPath(); got != "/tmp/custom-admin.json" {
		t.Errorf("credentialsPath() = %q", got)
	}
	t.Setenv("ADMIN_AUTH_FILE", "")
	t.Setenv("HOME", "/home/someone")
	if got := credentialsPath(); got != "/home/someone/.claude-whatsapp/admin.json" {
		t.Errorf("default path = %q", got)
	}
}
