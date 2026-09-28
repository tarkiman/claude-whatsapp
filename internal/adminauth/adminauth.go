// Package adminauth is the login system of the Admin UI: one account whose
// password is stored only as a salted PBKDF2 hash, in-memory sessions, and a
// limiter that slows down guessing.
//
// The Admin UI decides who may make Claude run commands on the machine, so the
// credentials live in their own 0600 file (default ~/.claude-whatsapp/admin.json)
// instead of .env, and can be changed from the UI or with `bin/admin passwd`.
package adminauth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	algo = "pbkdf2-sha256"
	// DefaultIterations follows OWASP's 2023 guidance for PBKDF2-HMAC-SHA256.
	DefaultIterations = 600_000
	keyLen            = 32
	saltLen           = 16

	MinPasswordLen = 10
	MaxPasswordLen = 200
	minUsernameLen = 3
	maxUsernameLen = 32
)

// Credentials is what is stored on disk. There is no plaintext anywhere.
type Credentials struct {
	Version    int    `json:"version"`
	Username   string `json:"username"`
	Algo       string `json:"algo"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"` // base64
	Hash       string `json:"hash"` // base64
	UpdatedAt  string `json:"updatedAt"`
}

// ValidateUsername accepts 3–32 characters of letters, digits, '.', '_' and '-'.
func ValidateUsername(u string) error {
	if n := utf8.RuneCountInString(u); n < minUsernameLen || n > maxUsernameLen {
		return fmt.Errorf("username must be %d–%d characters", minUsernameLen, maxUsernameLen)
	}
	for _, r := range u {
		ok := r == '.' || r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return errors.New("username may only contain letters, digits, '.', '_' and '-'")
		}
	}
	return nil
}

// MinDistinctChars keeps "aaaaaaaaaa" out without demanding symbols or digits.
const MinDistinctChars = 5

// PasswordRules is the requirement in plain words, shown wherever a password
// is chosen so nobody has to guess it from an error message.
const PasswordRules = "at least 10 characters, with at least 5 different ones — a short phrase of unrelated words works well"

// ValidatePassword enforces a sane minimum; the real protection is that the
// hash is slow and guessing is rate-limited. Every broken rule is reported at
// once, with the numbers, so one attempt is enough to learn what is needed.
func ValidatePassword(pw, username string) error {
	var problems []string
	n := utf8.RuneCountInString(pw)
	if n < MinPasswordLen {
		problems = append(problems, fmt.Sprintf("it is %d character(s) long and at least %d are needed", n, MinPasswordLen))
	}
	if n > MaxPasswordLen {
		problems = append(problems, fmt.Sprintf("it is longer than the %d characters allowed", MaxPasswordLen))
	}
	if username != "" && strings.EqualFold(pw, username) {
		problems = append(problems, "it is the same as the username")
	}
	distinct := map[rune]bool{}
	for _, r := range pw {
		distinct[r] = true
	}
	if len(distinct) < MinDistinctChars {
		problems = append(problems, fmt.Sprintf("it uses only %d different character(s) and at least %d are needed (something like aaaaaaaaaa is not accepted)", len(distinct), MinDistinctChars))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("password not accepted — %s. Use %s", strings.Join(problems, ", and "), PasswordRules)
}

func derive(password string, salt []byte, iterations int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, iterations, keyLen)
}

// --- credential store ---------------------------------------------------------

// Store reads and writes the credentials file. It notices changes made by
// another process (for example `bin/admin passwd` while the UI is running).
type Store struct {
	path       string
	iterations int

	mu     sync.Mutex
	cached Credentials
	have   bool
	err    error
	mtime  time.Time
	loaded bool
}

// Open returns a Store for path. iterations <= 0 selects DefaultIterations
// (tests pass a small value to stay fast).
func Open(path string, iterations int) *Store {
	if iterations <= 0 {
		iterations = DefaultIterations
	}
	return &Store{path: path, iterations: iterations}
}

func (s *Store) Path() string { return s.path }

func (s *Store) refreshLocked() {
	fi, err := os.Stat(s.path)
	if err != nil {
		s.cached, s.have, s.err, s.loaded = Credentials{}, false, nil, true
		s.mtime = time.Time{}
		return
	}
	if s.loaded && fi.ModTime().Equal(s.mtime) && (s.have || s.err != nil) {
		return
	}
	s.loaded, s.mtime = true, fi.ModTime()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		s.cached, s.have, s.err = Credentials{}, false, fmt.Errorf("read %s: %w", s.path, err)
		return
	}
	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		s.cached, s.have, s.err = Credentials{}, false, fmt.Errorf("parse %s: %w", s.path, err)
		return
	}
	if c.Algo != algo || c.Iterations < 1 || c.Username == "" || c.Salt == "" || c.Hash == "" {
		s.cached, s.have, s.err = Credentials{}, false, fmt.Errorf("invalid %s", s.path)
		return
	}
	s.cached, s.have, s.err = c, true, nil
}

// State reports whether credentials exist. err is set when the file exists but
// cannot be used — that must never be treated as "no password set yet".
func (s *Store) State() (exists bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.have, s.err
}

// Username of the account, or "" if none.
func (s *Store) Username() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.cached.Username
}

// Fingerprint identifies the current password. Sessions remember it, so a
// password change — from the UI or the command line — signs everybody out.
func (s *Store) Fingerprint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	if !s.have {
		return ""
	}
	return s.cached.Salt
}

// Verify checks a login. The work done does not depend on whether the
// username matched, so timing doesn't reveal it.
func (s *Store) Verify(username, password string) bool {
	s.mu.Lock()
	s.refreshLocked()
	c, have := s.cached, s.have
	s.mu.Unlock()
	if !have {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(c.Salt)
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(c.Hash)
	if err != nil {
		return false
	}
	got, err := derive(password, salt, c.Iterations)
	if err != nil {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(c.Username)) == 1
	passOK := subtle.ConstantTimeCompare(got, want) == 1
	return userOK && passOK
}

// Set validates and stores a new username/password.
func (s *Store) Set(username, password string) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	if err := ValidatePassword(password, username); err != nil {
		return err
	}
	return s.write(username, password)
}

func (s *Store) write(username, password string) error {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key, err := derive(password, salt, s.iterations)
	if err != nil {
		return err
	}
	c := Credentials{
		Version: 1, Username: username, Algo: algo, Iterations: s.iterations,
		Salt: base64.StdEncoding.EncodeToString(salt), Hash: base64.StdEncoding.EncodeToString(key),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".admin-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	s.mu.Lock()
	s.loaded = false
	s.refreshLocked()
	s.mu.Unlock()
	return nil
}

// Bootstrap adopts a password given the old way (ADMIN_PASSWORD in .env) when
// no credentials file exists yet, so an upgrade keeps working. The value is
// hashed, never written back in the clear. The password-strength policy is
// not applied: it is the password the operator already uses.
func Bootstrap(s *Store, username, password string) (adopted bool, err error) {
	if password == "" {
		return false, nil
	}
	if exists, ferr := s.State(); exists || ferr != nil {
		return false, nil
	}
	if username == "" {
		username = "admin"
	}
	if err := ValidateUsername(username); err != nil {
		return false, err
	}
	if err := s.write(username, password); err != nil {
		return false, err
	}
	return true, nil
}

// --- sessions -------------------------------------------------------------------

type session struct {
	created, last time.Time
	fp            string
}

// Sessions are random tokens kept in memory: restarting the admin signs
// everybody out, which is fine for a single-operator tool.
type Sessions struct {
	mu        sync.Mutex
	now       func() time.Time
	idle, max time.Duration
	m         map[string]*session
}

func NewSessions(idle, max time.Duration) *Sessions {
	return &Sessions{now: time.Now, idle: idle, max: max, m: map[string]*session{}}
}

// Create starts a session bound to the current password fingerprint.
func (s *Sessions) Create(fp string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // no entropy: nothing sensible to do
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.m { // drop expired ones as we go
		if s.expired(v, now) {
			delete(s.m, k)
		}
	}
	s.m[tok] = &session{created: now, last: now, fp: fp}
	return tok
}

func (s *Sessions) expired(v *session, now time.Time) bool {
	return now.Sub(v.last) > s.idle || now.Sub(v.created) > s.max
}

// Valid reports whether token is a live session for the current password, and
// counts the request as activity.
func (s *Sessions) Valid(token, fp string) bool {
	if token == "" || fp == "" {
		return false
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[token]
	if !ok {
		return false
	}
	if s.expired(v, now) || v.fp != fp {
		delete(s.m, token)
		return false
	}
	v.last = now
	return true
}

func (s *Sessions) Revoke(token string) {
	s.mu.Lock()
	delete(s.m, token)
	s.mu.Unlock()
}

func (s *Sessions) RevokeAll() {
	s.mu.Lock()
	s.m = map[string]*session{}
	s.mu.Unlock()
}

// --- guess limiter ---------------------------------------------------------------

type entry struct {
	fails       int
	first       time.Time
	lockedUntil time.Time
	lockouts    int
	touched     time.Time
}

// Limiter locks a client out after too many wrong passwords. The lock doubles
// each time it is triggered again (up to an hour), and a successful login
// clears it.
type Limiter struct {
	mu     sync.Mutex
	now    func() time.Time
	max    int
	window time.Duration
	lock   time.Duration
	m      map[string]*entry
}

func NewLimiter(maxFails int, window, lock time.Duration) *Limiter {
	return &Limiter{now: time.Now, max: maxFails, window: window, lock: lock, m: map[string]*entry{}}
}

// Check reports whether key may try now; if not, how long to wait.
func (l *Limiter) Check(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[key]
	if !ok {
		return true, 0
	}
	if wait := e.lockedUntil.Sub(l.now()); wait > 0 {
		return false, wait
	}
	return true, 0
}

func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.m) > 10_000 {
		for k, v := range l.m {
			if now.Sub(v.touched) > time.Hour && v.lockedUntil.Before(now) {
				delete(l.m, k)
			}
		}
	}
	e, ok := l.m[key]
	if !ok {
		e = &entry{first: now}
		l.m[key] = e
	}
	e.touched = now
	if now.Sub(e.first) > l.window && e.lockedUntil.Before(now) {
		e.fails, e.first = 0, now
	}
	e.fails++
	if e.fails >= l.max {
		e.lockouts++
		d := l.lock
		for i := 1; i < e.lockouts && d < time.Hour; i++ {
			d *= 2
		}
		if d > time.Hour {
			d = time.Hour
		}
		e.lockedUntil = now.Add(d)
		e.fails, e.first = 0, now
	}
}

func (l *Limiter) Success(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}
