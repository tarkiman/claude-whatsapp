package jobs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/claude"
)

type fakeRunner struct {
	mu      sync.Mutex
	runs    []claude.Run
	release chan struct{} // closed to let runs finish; nil = finish at once
	text    string
	err     error
}

func (f *fakeRunner) Stream(ctx context.Context, run claude.Run) (string, string, error) {
	f.mu.Lock()
	f.runs = append(f.runs, run)
	f.mu.Unlock()
	if run.OnTool != nil {
		run.OnTool(claude.ToolUse{Name: "Bash", Input: map[string]any{"command": "go test ./..."}})
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	return f.text, "sess-" + filepath.Base(run.Dir), f.err
}

type memSessions struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memSessions) Get(k string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok
}
func (s *memSessions) Set(k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
	return nil
}

type sink struct {
	mu   sync.Mutex
	said []string
	sent []string
	done chan struct{}
}

func newSink() *sink            { return &sink{done: make(chan struct{}, 10)} }
func (s *sink) notify(t string) { s.mu.Lock(); s.said = append(s.said, t); s.mu.Unlock() }
func (s *sink) detail(t string) {
	s.mu.Lock()
	s.sent = append(s.sent, t)
	s.mu.Unlock()
	s.done <- struct{}{}
}
func (s *sink) wait(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no report within 5 s")
	}
}

func projectsDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(dir, n, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.MkdirAll(filepath.Join(dir, "not-a-repo"), 0o755)
	return dir
}

func newManager(t *testing.T, r Runner, s *sink, allowed ...string) (*Manager, *memSessions) {
	sess := &memSessions{m: map[string]string{}}
	m := New(Options{ProjectsDir: projectsDir(t, "media-nest", "islamic-content"), Allowed: allowed,
		Timeout: time.Minute, MaxRunning: 2, Notify: s.notify, Detail: s.detail}, r, sess)
	return m, sess
}

func TestProjectsAndResolve(t *testing.T) {
	m, _ := newManager(t, &fakeRunner{}, newSink(), "*")
	var names []string
	for _, p := range m.Projects() {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "islamic-content,media-nest" {
		t.Errorf("projects: got %v, want the two git repos only", names)
	}
	for _, spoken := range []string{"media nest", "Media-Nest", "medianest"} {
		if got, ok := m.resolve(spoken); !ok || got != "media-nest" {
			t.Errorf("resolve(%q) = %q, %v; want media-nest", spoken, got, ok)
		}
	}
	only, _ := newManager(t, &fakeRunner{}, newSink(), "media-nest")
	if _, err := only.Start("islamic-content", "x", false); !errors.Is(err, ErrUnknownProject) {
		t.Errorf("project outside the allowlist: got %v, want ErrUnknownProject", err)
	}
}

func TestJobRunsReportsAndResumes(t *testing.T) {
	s := newSink()
	r := &fakeRunner{text: "Sudah.\nRINGKASAN: Eca sudah menambah tombol acak. Tesnya lulus.\nDETAIL:\n- PlaylistView.vue diubah\n- go test lulus"}
	m, sess := newManager(t, r, s, "*")

	j, err := m.Start("media nest", "tambah tombol acak", false)
	if err != nil || j.Project != "media-nest" || j.Status != Running {
		t.Fatalf("Start: %+v, %v", j, err)
	}
	s.wait(t)
	got, _ := m.Get(j.ID)
	if got.Status != Done || got.Summary != "Eca sudah menambah tombol acak. Tesnya lulus." || !strings.Contains(got.Detail, "PlaylistView.vue") {
		t.Errorf("finished job: %+v", got)
	}
	if got.Activity != "menjalankan tes" || got.Steps != 1 {
		t.Errorf("activity: got %q after %d steps", got.Activity, got.Steps)
	}
	if !strings.HasPrefix(s.said[0], "Kerjaan di media nest selesai. Eca sudah menambah") || !strings.Contains(s.said[0], "WhatsApp") {
		t.Errorf("spoken report: %q", s.said[0])
	}
	if strings.Contains(strings.ToLower(s.said[0]), "claude") {
		t.Errorf("spoken report names the tool: %q", s.said[0])
	}
	if !strings.Contains(s.sent[0], "media-nest") || !strings.Contains(s.sent[0], "go test lulus") {
		t.Errorf("WhatsApp report: %q", s.sent[0])
	}
	if v, _ := sess.Get("media-nest"); v != "sess-media-nest" {
		t.Errorf("session saved: %q", v)
	}

	// The next job resumes it; a new session does not.
	m.Start("media-nest", "lanjut", false)
	s.wait(t)
	m.Start("media-nest", "baru", true)
	s.wait(t)
	if r.runs[1].Resume != "sess-media-nest" || r.runs[2].Resume != "" {
		t.Errorf("resume: got %q then %q", r.runs[1].Resume, r.runs[2].Resume)
	}
	if r.runs[0].AppendSystem != SystemPrompt || !strings.HasSuffix(r.runs[0].Dir, "media-nest") {
		t.Errorf("run: %+v", r.runs[0])
	}
}

func TestBusyCancelAndFailure(t *testing.T) {
	s := newSink()
	r := &fakeRunner{release: make(chan struct{})}
	m, _ := newManager(t, r, s, "*")

	j, _ := m.Start("media-nest", "lama", false)
	var busy *BusyError
	if _, err := m.Start("media-nest", "lagi", false); !errors.As(err, &busy) || busy.Job.ID != j.ID {
		t.Errorf("second job on the same project: got %v, want busy with %s", err, j.ID)
	}
	if _, err := m.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	s.wait(t)
	if got, _ := m.Get(j.ID); got.Status != Cancelled {
		t.Errorf("cancelled job: %+v", got)
	}
	if !strings.Contains(s.said[0], "hentikan") {
		t.Errorf("cancel report: %q", s.said[0])
	}

	f := &fakeRunner{err: errors.New("claude exited: exit status 1 (stderr: boom)")}
	fm, _ := newManager(t, f, s, "*")
	fj, _ := fm.Start("islamic-content", "x", false)
	s.wait(t)
	if got, _ := fm.Get(fj.ID); got.Status != Failed || !strings.Contains(s.said[1], "gagal") {
		t.Errorf("failed job: %+v, said %q", got, s.said[1])
	}
	if _, err := fm.Start("islamic-content", "  ", false); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty instruction: %v", err)
	}
}

func TestParseReport(t *testing.T) {
	sum, det := ParseReport("RINGKASAN:\nEca sudah selesai, mau Eca **push**? Lihat https://x.y\nDETAIL:\nfile a.go")
	if sum != "Eca sudah selesai, mau Eca push? Lihat" || det != "file a.go" {
		t.Errorf("got %q / %q", sum, det)
	}
	sum, det = ParseReport("Semua beres. Tes lulus.")
	if sum != "Semua beres. Tes lulus." || det != "Semua beres. Tes lulus." {
		t.Errorf("no sections: got %q / %q", sum, det)
	}
	sum, det = ParseReport("Ubah tiga file.\n\nRINGKASAN: Sudah.")
	if sum != "Sudah." || det != "Ubah tiga file." {
		t.Errorf("no DETAIL: got %q / %q", sum, det)
	}
}

func TestDescribe(t *testing.T) {
	cases := map[string]claude.ToolUse{
		"mengubah main.go":             {Name: "Edit", Input: map[string]any{"file_path": "/x/cmd/main.go"}},
		"menjalankan tes":              {Name: "Bash", Input: map[string]any{"command": "cd x && go test ./..."}},
		"mengirim ke GitHub":           {Name: "Bash", Input: map[string]any{"command": "git push -u origin feat"}},
		"membuat commit":               {Name: "Bash", Input: map[string]any{"command": "git commit -m x"}},
		"mencari di kode":              {Name: "Grep", Input: map[string]any{"pattern": "x"}},
		"menjalankan perintah ls":      {Name: "Bash", Input: map[string]any{"command": "ls -la"}},
		"membangun aplikasi":           {Name: "Bash", Input: map[string]any{"command": "cd frontend && npm run build"}},
		"mengetes tampilan di browser": {Name: "Bash", Input: map[string]any{"command": "/usr/bin/chromium --headless=new about:blank"}},
		"memeriksa kode":               {Name: "Bash", Input: map[string]any{"command": "cd frontend && cd src && npx vue-tsc --noEmit"}},
		"berpindah folder":             {Name: "Bash", Input: map[string]any{"command": "cd /tmp"}},
	}
	for want, tool := range cases {
		if got := Describe(tool); got != want {
			t.Errorf("Describe(%v) = %q, want %q", tool, got, want)
		}
	}
}

func TestHTTPNeedsToken(t *testing.T) {
	m, _ := newManager(t, &fakeRunner{}, newSink(), "*")
	h := Handler(m, "secret-token-123456789012345")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/projects", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", rec.Code)
	}
	req := httptest.NewRequest("POST", "/jobs", strings.NewReader(`{"project":"nope","instruction":"x"}`))
	req.Header.Set("Authorization", "Bearer secret-token-123456789012345")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "media-nest") {
		t.Errorf("unknown project: %d %s", rec.Code, rec.Body.String())
	}
}

func TestProgressOnlyWhenMovingOn(t *testing.T) {
	s := newSink()
	r := &fakeRunner{release: make(chan struct{})}
	sess := &memSessions{m: map[string]string{}}
	m := New(Options{ProjectsDir: projectsDir(t, "media-nest"), Allowed: []string{"*"}, Timeout: time.Minute,
		MaxRunning: 1, ProgressEvery: 30 * time.Millisecond, Notify: s.notify, Detail: s.detail}, r, sess)
	m.Start("media-nest", "x", false)
	time.Sleep(200 * time.Millisecond) // ~6 ticks, but the fake made one tool call only
	close(r.release)
	s.wait(t)
	progress := 0
	for _, said := range s.said {
		if strings.Contains(said, "masih jalan") {
			progress++
		}
	}
	if progress != 1 {
		t.Errorf("got %d progress notes for a job that moved on once, want 1: %q", progress, s.said)
	}
}
