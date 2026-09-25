package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Claude sign-in from the UI drives `claude auth login`, which prints an
// authorize URL, then waits on stdin for the code the callback page shows
// ("Paste code here if prompted >"). The PKCE verifier never leaves that
// child process, so the pasted code is useless to anyone who only sees it.

const (
	loginStartWait = 15 * time.Second
	loginMaxAge    = 10 * time.Minute
	loginKeepLines = 30
	maxCodeLen     = 4096
)

var loginURLRe = regexp.MustCompile(`https://\S+`)

var loginMethods = map[string]string{
	"claudeai": "--claudeai", // Claude subscription
	"console":  "--console",  // Anthropic Console (API billing)
}

const (
	loginIdle        = "idle"
	loginWaitingCode = "waiting_code"
	loginVerifying   = "verifying"
	loginDone        = "done"
	loginFailed      = "failed"
)

type loginSession struct {
	mu      sync.Mutex
	state   string
	url     string
	errMsg  string
	lines   []string
	secrets []string
	started time.Time
	stdin   io.WriteCloser
	cancel  context.CancelFunc
	gen     int // bumped per run so a finished old run can't clobber a new one
}

type LoginSnapshot struct {
	State   string   `json:"state"`
	URL     string   `json:"url,omitempty"`
	Error   string   `json:"error,omitempty"`
	Output  []string `json:"output,omitempty"`
	Started string   `json:"started,omitempty"`
}

func (l *loginSession) snapshotLocked() LoginSnapshot {
	snap := LoginSnapshot{State: l.state, URL: l.url, Error: l.errMsg}
	if l.state == "" {
		snap.State = loginIdle
	}
	if l.state == loginFailed || l.state == loginDone {
		snap.Output = append([]string(nil), l.lines...)
	}
	if !l.started.IsZero() {
		snap.Started = l.started.Format(time.RFC3339)
	}
	return snap
}

func (l *loginSession) activeLocked() bool {
	return l.state == loginWaitingCode || l.state == loginVerifying || l.state == "starting"
}

// addLine records CLI output, redacting anything the operator pasted so the
// authorization code never reaches the UI or logs.
func (l *loginSession) addLine(gen int, line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if gen != l.gen {
		return
	}
	for _, s := range l.secrets {
		line = strings.ReplaceAll(line, s, "[redacted]")
	}
	l.lines = append(l.lines, line)
	if len(l.lines) > loginKeepLines {
		l.lines = l.lines[len(l.lines)-loginKeepLines:]
	}
	if l.url == "" && l.state == "starting" {
		if u := loginURLRe.FindString(line); u != "" {
			l.url = u
			l.state = loginWaitingCode
		}
	}
}

func (s *Server) startLogin(method string) (LoginSnapshot, error) {
	flag, ok := loginMethods[method]
	if !ok {
		return LoginSnapshot{}, errors.New(`method must be "claudeai" or "console"`)
	}

	l := &s.login
	l.mu.Lock()
	if l.activeLocked() {
		snap := l.snapshotLocked()
		l.mu.Unlock()
		return snap, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), loginMaxAge)
	cmd := exec.CommandContext(ctx, s.cfg.ClaudeBin, "auth", "login", flag)
	// Never try to pop a browser open on the Pi's own desktop; the operator
	// opens the link on their own device.
	cmd.Env = append(cmd.Environ(), "DISPLAY=", "WAYLAND_DISPLAY=", "BROWSER=true")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		l.mu.Unlock()
		return LoginSnapshot{}, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw

	if err := cmd.Start(); err != nil {
		cancel()
		l.mu.Unlock()
		return LoginSnapshot{}, err
	}
	l.gen++
	gen := l.gen
	l.state, l.url, l.errMsg = "starting", "", ""
	l.lines, l.secrets = nil, nil
	l.started = time.Now()
	l.stdin, l.cancel = stdin, cancel
	l.mu.Unlock()

	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			l.addLine(gen, sc.Text())
		}
	}()
	go func() {
		werr := cmd.Wait()
		_ = pw.Close()
		cancel()
		l.mu.Lock()
		defer l.mu.Unlock()
		if gen != l.gen {
			return
		}
		l.stdin = nil
		switch {
		case werr == nil:
			l.state = loginDone
			s.invalidateClaudeCache()
		case l.state == loginIdle:
			// cancelled by the operator; state already reset
		default:
			l.state = loginFailed
			l.errMsg = "login did not complete"
			if ctx.Err() == context.DeadlineExceeded {
				l.errMsg = "login timed out"
			}
		}
	}()

	deadline := time.Now().Add(loginStartWait)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		state := l.state
		snap := l.snapshotLocked()
		l.mu.Unlock()
		if state == loginWaitingCode || state == loginFailed || state == loginDone {
			return snap, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.cancelLogin()
	return LoginSnapshot{}, errors.New("`claude auth login` did not print a sign-in link in time")
}

func (s *Server) submitLoginCode(code string) (LoginSnapshot, error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > maxCodeLen || strings.ContainsAny(code, "\r\n") {
		return LoginSnapshot{}, errors.New("paste the single-line code shown after signing in")
	}
	l := &s.login
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != loginWaitingCode || l.stdin == nil {
		return LoginSnapshot{}, errors.New("no sign-in is waiting for a code — start one first")
	}
	l.secrets = append(l.secrets, code)
	if _, err := io.WriteString(l.stdin, code+"\n"); err != nil {
		return LoginSnapshot{}, err
	}
	l.state = loginVerifying
	return l.snapshotLocked(), nil
}

func (s *Server) cancelLogin() {
	l := &s.login
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		l.state = loginIdle
		l.url = ""
		l.cancel()
	}
}

func (s *Server) invalidateClaudeCache() {
	s.claudeMu.Lock()
	s.claudeVal = nil
	s.claudeMu.Unlock()
}

func (s *Server) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
	}
	_ = decodeBody(r, &req)
	if req.Method == "" {
		req.Method = "claudeai"
	}
	snap, err := s.startLogin(req.Method)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleLoginCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	_ = decodeBody(r, &req)
	snap, err := s.submitLoginCode(req.Code)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, snap)
}

func (s *Server) handleLoginCancel(w http.ResponseWriter, _ *http.Request) {
	s.cancelLogin()
	writeJSON(w, http.StatusOK, map[string]string{"ok": "cancelled"})
}

func (s *Server) handleLoginState(w http.ResponseWriter, _ *http.Request) {
	s.login.mu.Lock()
	snap := s.login.snapshotLocked()
	s.login.mu.Unlock()
	writeJSON(w, http.StatusOK, snap)
}

func decodeBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(v)
}
