package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/config"
)

// fakeClaude mimics the transcript of the real `claude auth login --claudeai`
// (link, then a stdin prompt) so the flow is testable without touching real
// credentials.
func fakeClaude(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
echo "Opening browser to sign in…"
echo "If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&state=abc"
printf "Paste code here if prompted > "
read code
if [ "$code" = "goodcode" ]; then
  echo "Login successful (got $code)"
  exit 0
fi
echo "Invalid code" >&2
exit 1
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newLoginServer(t *testing.T) *Server {
	t.Helper()
	return New(&config.Config{ClaudeBin: fakeClaude(t)}, nil, Options{})
}

func waitState(t *testing.T, s *Server, want string) LoginSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.login.mu.Lock()
		snap := s.login.snapshotLocked()
		s.login.mu.Unlock()
		if snap.State == want {
			return snap
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("state never became %q", want)
	return LoginSnapshot{}
}

func TestLoginSuccess(t *testing.T) {
	s := newLoginServer(t)

	snap, err := s.startLogin("claudeai")
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != loginWaitingCode || !strings.HasPrefix(snap.URL, "https://claude.com/cai/oauth/authorize?") {
		t.Fatalf("unexpected start snapshot: %+v", snap)
	}

	// Starting again while one is active must reuse it, not spawn a second CLI.
	again, err := s.startLogin("claudeai")
	if err != nil || again.URL != snap.URL {
		t.Fatalf("second start: %+v, %v", again, err)
	}

	if _, err := s.submitLoginCode("goodcode"); err != nil {
		t.Fatal(err)
	}
	done := waitState(t, s, loginDone)
	joined := strings.Join(done.Output, "\n")
	if strings.Contains(joined, "goodcode") {
		t.Errorf("pasted code leaked into output: %q", joined)
	}
	if !strings.Contains(joined, "[redacted]") {
		t.Errorf("expected redacted code in output: %q", joined)
	}
}

func TestLoginBadCode(t *testing.T) {
	s := newLoginServer(t)
	if _, err := s.startLogin("claudeai"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.submitLoginCode("wrong"); err != nil {
		t.Fatal(err)
	}
	snap := waitState(t, s, loginFailed)
	if snap.Error == "" {
		t.Error("failed login should carry an error message")
	}
}

func TestLoginGuards(t *testing.T) {
	s := newLoginServer(t)

	if _, err := s.submitLoginCode("x"); err == nil {
		t.Error("code accepted with no sign-in running")
	}
	if _, err := s.startLogin("nonsense"); err == nil {
		t.Error("unknown method accepted")
	}
	if _, err := s.startLogin("claudeai"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "   ", "a\nb"} {
		if _, err := s.submitLoginCode(bad); err == nil {
			t.Errorf("code %q accepted", bad)
		}
	}
}

func TestLoginCancelThenRestart(t *testing.T) {
	s := newLoginServer(t)
	if _, err := s.startLogin("claudeai"); err != nil {
		t.Fatal(err)
	}
	s.cancelLogin()
	waitState(t, s, loginIdle)

	// A fresh run after cancel must not be clobbered by the old run's cleanup.
	snap, err := s.startLogin("claudeai")
	if err != nil || snap.State != loginWaitingCode {
		t.Fatalf("restart: %+v, %v", snap, err)
	}
	time.Sleep(200 * time.Millisecond)
	s.login.mu.Lock()
	state := s.login.state
	s.login.mu.Unlock()
	if state != loginWaitingCode {
		t.Fatalf("old run clobbered the new one: state=%q", state)
	}
	s.cancelLogin()
}
