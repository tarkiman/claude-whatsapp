// Package jobs runs Claude Code tasks in the background for a voice front end
// (Eca, through kawan-bridge in the virtual-assistant repo): a task is started
// with one HTTP call, runs in the project's directory under its own session,
// and reports back through a notify URL (a short spoken summary) and WhatsApp
// (the full detail). Voice sessions are kept apart from the WhatsApp chats'
// sessions, one per project.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/claude"
)

// Runner is the part of claude.Runner a job needs (a fake in tests).
type Runner interface {
	Stream(ctx context.Context, run claude.Run) (text, sessionID string, err error)
}

// Sessions remembers the session to resume per project.
type Sessions interface {
	Get(key string) (string, bool)
	Set(key, sessionID string) error
}

// Options configure a Manager.
type Options struct {
	ProjectsDir   string            // projects are the git repositories directly in here
	Allowed       []string          // project names allowed, or "*" for every git repository in ProjectsDir
	Timeout       time.Duration     // per job
	MaxRunning    int               // across all projects
	ProgressEvery time.Duration     // a spoken progress note this often while a job runs (0 = never)
	ProgressMax   int               // at most this many progress notes per job
	Notify        func(text string) // spoken report (kawan-bridge /notify); may be nil
	Detail        func(text string) // full report (WhatsApp); may be nil
}

// Status of a job.
const (
	Running   = "running"
	Done      = "done"
	Failed    = "failed"
	Cancelled = "cancelled"
)

// Job is one task and what came of it.
type Job struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"`
	Instruction string    `json:"instruction"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitzero"`
	Activity    string    `json:"activity,omitempty"` // what it is doing now, in spoken Indonesian
	Steps       int       `json:"steps"`              // tool calls so far
	Summary     string    `json:"summary,omitempty"`  // spoken summary (RINGKASAN)
	Detail      string    `json:"detail,omitempty"`   // full report (DETAIL)
	Error       string    `json:"error,omitempty"`
	NewSession  bool      `json:"new_session"`

	cancel context.CancelFunc
}

var (
	ErrUnknownProject = errors.New("unknown or not allowed project")
	ErrBusy           = errors.New("a job is already running")
	ErrNotFound       = errors.New("no such job")
	ErrEmpty          = errors.New("empty instruction")
)

// BusyError names the job that is in the way.
type BusyError struct{ Job Job }

func (e *BusyError) Error() string {
	return fmt.Sprintf("%v: %s on %s", ErrBusy, e.Job.ID, e.Job.Project)
}
func (e *BusyError) Unwrap() error { return ErrBusy }

// Manager starts, follows and cancels jobs. History is kept in memory (the last 50 jobs).
type Manager struct {
	opt      Options
	runner   Runner
	sessions Sessions

	mu      sync.Mutex
	jobs    map[string]*Job
	order   []string
	running map[string]string // project -> job id
}

func New(opt Options, runner Runner, sessions Sessions) *Manager {
	if opt.MaxRunning < 1 {
		opt.MaxRunning = 1
	}
	return &Manager{opt: opt, runner: runner, sessions: sessions, jobs: map[string]*Job{}, running: map[string]string{}}
}

// Project is a repository the voice front end may work in.
type Project struct {
	Name       string `json:"name"`
	HasSession bool   `json:"has_session"`
	RunningJob string `json:"running_job,omitempty"`
}

// Projects lists the allowed projects, alphabetically.
func (m *Manager) Projects() []Project {
	var names []string
	if len(m.opt.Allowed) == 1 && m.opt.Allowed[0] == "*" {
		entries, _ := os.ReadDir(m.opt.ProjectsDir)
		for _, e := range entries {
			if e.IsDir() && isRepo(filepath.Join(m.opt.ProjectsDir, e.Name())) {
				names = append(names, e.Name())
			}
		}
	} else {
		for _, n := range m.opt.Allowed {
			if isRepo(filepath.Join(m.opt.ProjectsDir, n)) {
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Project, 0, len(names))
	for _, n := range names {
		_, has := m.sessions.Get(n)
		out = append(out, Project{Name: n, HasSession: has, RunningJob: m.running[n]})
	}
	return out
}

func isRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// resolve finds the project by name, forgiving case and spaces/dashes ("media nest" -> media-nest).
func (m *Manager) resolve(name string) (string, bool) {
	want := squash(name)
	for _, p := range m.Projects() {
		if squash(p.Name) == want {
			return p.Name, true
		}
	}
	return "", false
}

func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == ' ' || r == '.' {
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(s)))
}

// Start begins a job and returns at once; the job runs in the background.
func (m *Manager) Start(project, instruction string, newSession bool) (Job, error) {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return Job{}, ErrEmpty
	}
	name, ok := m.resolve(project)
	if !ok {
		return Job{}, ErrUnknownProject
	}
	m.mu.Lock()
	if id, busy := m.running[name]; busy {
		j := *m.jobs[id]
		m.mu.Unlock()
		return Job{}, &BusyError{Job: j}
	}
	if len(m.running) >= m.opt.MaxRunning {
		var other Job
		for _, id := range m.running {
			other = *m.jobs[id]
		}
		m.mu.Unlock()
		return Job{}, &BusyError{Job: other}
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.opt.Timeout)
	j := &Job{ID: newID(), Project: name, Instruction: instruction, Status: Running, StartedAt: time.Now(),
		Activity: "baru mulai", NewSession: newSession, cancel: cancel}
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	if len(m.order) > 50 {
		old := m.order[0]
		m.order = m.order[1:]
		if m.jobs[old].Status != Running {
			delete(m.jobs, old)
		}
	}
	m.running[name] = j.ID
	snapshot := *j
	m.mu.Unlock()

	go m.run(ctx, j)
	return snapshot, nil
}

func (m *Manager) run(ctx context.Context, j *Job) {
	defer j.cancel()
	resume := ""
	if !j.NewSession {
		resume, _ = m.sessions.Get(j.Project)
	}
	stopProgress := m.progress(ctx, j)
	text, sid, err := m.runner.Stream(ctx, claude.Run{
		Dir:          filepath.Join(m.opt.ProjectsDir, j.Project),
		Prompt:       j.Instruction,
		Resume:       resume,
		AppendSystem: SystemPrompt,
		OnTool: func(t claude.ToolUse) {
			m.mu.Lock()
			j.Steps++
			j.Activity = Describe(t)
			m.mu.Unlock()
		},
	})
	stopProgress()

	m.mu.Lock()
	j.FinishedAt = time.Now()
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		j.Status, j.Error = Cancelled, "dibatalkan"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		j.Status, j.Error = Failed, fmt.Sprintf("melewati batas waktu %s", m.opt.Timeout)
	case err != nil:
		j.Status, j.Error = Failed, err.Error()
	default:
		j.Status = Done
		j.Summary, j.Detail = ParseReport(text)
	}
	delete(m.running, j.Project)
	done := *j
	m.mu.Unlock()

	if done.Status == Done && sid != "" {
		if err := m.sessions.Set(done.Project, sid); err != nil {
			log.Printf("jobs: saving session for %s: %v", done.Project, err)
		}
	}
	log.Printf("jobs: %s on %s %s after %s (%d steps)", done.ID, done.Project, done.Status,
		done.FinishedAt.Sub(done.StartedAt).Round(time.Second), done.Steps)
	m.report(done)
}

// progress sends a spoken note every ProgressEvery while the job runs, but only when it has moved on since the
// last note (new tool calls), so a long quiet step (a build, a browser test) is not announced twice. ProgressMax
// caps the notes per job; 0 means no cap (the job's Timeout still bounds it).
func (m *Manager) progress(ctx context.Context, j *Job) (stop func()) {
	if m.opt.ProgressEvery <= 0 || m.opt.Notify == nil {
		return func() {}
	}
	quit := make(chan struct{})
	go func() {
		t := time.NewTicker(m.opt.ProgressEvery)
		defer t.Stop()
		lastSteps := -1
		for sent := 0; m.opt.ProgressMax == 0 || sent < m.opt.ProgressMax; {
			select {
			case <-quit:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				m.mu.Lock()
				activity, steps, mins := j.Activity, j.Steps, int(time.Since(j.StartedAt).Minutes())
				m.mu.Unlock()
				if steps == lastSteps {
					continue
				}
				lastSteps = steps
				m.opt.Notify(fmt.Sprintf("Kerjaan di %s masih jalan, sudah %d menit. Sekarang Eca %s.", spoken(j.Project), mins, activity))
				sent++
			}
		}
	}()
	return func() { close(quit) }
}

func (m *Manager) report(j Job) {
	took := j.FinishedAt.Sub(j.StartedAt).Round(time.Second)
	var say, wa string
	switch j.Status {
	case Done:
		say = fmt.Sprintf("Kerjaan di %s selesai. %s", spoken(j.Project), j.Summary)
		if j.Detail != "" && m.opt.Detail != nil {
			say += " Rinciannya sudah Eca kirim ke WhatsApp."
		}
		wa = fmt.Sprintf("🎙️ *%s* selesai (%s)\n\n_Instruksi:_ %s\n\n%s", j.Project, took, j.Instruction, firstNonEmpty(j.Detail, j.Summary))
	case Cancelled:
		say = fmt.Sprintf("Kerjaan di %s sudah Eca hentikan.", spoken(j.Project))
		wa = fmt.Sprintf("🎙️ *%s* dibatalkan setelah %s\n\n_Instruksi:_ %s", j.Project, took, j.Instruction)
	default:
		say = fmt.Sprintf("Maaf Abi, kerjaan di %s gagal: %s", spoken(j.Project), shortError(j.Error))
		wa = fmt.Sprintf("🎙️ *%s* gagal setelah %s\n\n_Instruksi:_ %s\n\n```%s```", j.Project, took, j.Instruction, j.Error)
	}
	if m.opt.Notify != nil {
		m.opt.Notify(say)
	}
	if m.opt.Detail != nil {
		m.opt.Detail(wa)
	}
}

// Get returns a job by id.
func (m *Manager) Get(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	return *j, nil
}

// List returns recent jobs, newest first, optionally for one project.
func (m *Manager) List(project string, limit int) []Job {
	name := ""
	if project != "" {
		name, _ = m.resolve(project)
		if name == "" {
			return nil
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Job
	for i := len(m.order) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		if j, ok := m.jobs[m.order[i]]; ok && (name == "" || j.Project == name) {
			out = append(out, *j)
		}
	}
	return out
}

// Cancel stops a running job; its report says so.
func (m *Manager) Cancel(id string) (Job, error) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return Job{}, ErrNotFound
	}
	snapshot := *j
	m.mu.Unlock()
	if snapshot.Status == Running {
		j.cancel()
	}
	return snapshot, nil
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// spoken turns a repo name into something TTS reads well: "media-nest" -> "media nest".
func spoken(project string) string {
	return strings.NewReplacer("-", " ", "_", " ").Replace(project)
}

func shortError(e string) string {
	if i := strings.IndexAny(e, "\n("); i > 0 {
		e = e[:i]
	}
	if len(e) > 160 {
		e = e[:160]
	}
	return strings.TrimSpace(e) + ". Rinciannya ada di WhatsApp."
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
