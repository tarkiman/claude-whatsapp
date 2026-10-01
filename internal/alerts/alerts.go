// Package alerts sends a Discord message when the bridge stack's overall
// status changes (ok -> degraded/down, and back to ok), so a problem such as
// WhatsApp disconnecting is noticed without anyone opening the Admin UI.
package alerts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Config is the alerting setup: whether it is on, and where to send to.
type Config struct {
	Version    int    `json:"version"`
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhookUrl"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
}

// webhookRe accepts Discord's webhook URL shape on either of its two valid
// hostnames (discordapp.com is the older, still-functional alias). Scoping
// validation to this pattern keeps the field from being used as a general
// SSRF-style relay to an arbitrary host.
var webhookRe = regexp.MustCompile(`^https://(discord\.com|discordapp\.com)/api/webhooks/[0-9]{5,25}/[\w-]{20,}(/[\w-]*)?$`)

// ValidateWebhookURL checks the shape of a Discord webhook URL without
// calling it; use Notifier.Send (e.g. a test alert) to confirm it is live.
func ValidateWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("webhook URL is empty")
	}
	if !webhookRe.MatchString(raw) {
		return errors.New(`doesn't look like a Discord webhook URL — expected https://discord.com/api/webhooks/<id>/<token> (copy it from Discord: channel Settings → Integrations → Webhooks)`)
	}
	return nil
}

// Masked is safe to show back in a UI or log: enough to recognise which
// webhook is configured, never enough to post as it.
func Masked(raw string) string {
	u, err := extractParts(raw)
	if err != nil {
		return ""
	}
	tok := u.token
	if len(tok) > 6 {
		tok = tok[:3] + "…" + tok[len(tok)-3:]
	}
	return fmt.Sprintf("https://%s/api/webhooks/%s/%s", u.host, u.id, tok)
}

type webhookParts struct{ host, id, token string }

func extractParts(raw string) (webhookParts, error) {
	if err := ValidateWebhookURL(raw); err != nil {
		return webhookParts{}, err
	}
	rest := strings.TrimPrefix(raw, "https://")
	host, rest, _ := strings.Cut(rest, "/api/webhooks/")
	id, token, _ := strings.Cut(rest, "/")
	token, _, _ = strings.Cut(token, "/")
	return webhookParts{host: host, id: id, token: token}, nil
}

// --- storage ------------------------------------------------------------------

// Store holds the alert configuration and reloads it when the file changes,
// same pattern as internal/access.
type Store struct {
	path string

	mu    sync.Mutex
	cfg   Config
	mtime time.Time
	err   error
	init  bool
}

func Open(path string) *Store { return &Store{path: path} }

func (s *Store) Path() string { return s.path }

func (s *Store) refreshLocked() {
	fi, err := os.Stat(s.path)
	if err != nil {
		s.cfg, s.err, s.init = Config{}, nil, true
		s.mtime = time.Time{}
		return
	}
	if s.init && fi.ModTime().Equal(s.mtime) && s.err == nil {
		return
	}
	s.init, s.mtime = true, fi.ModTime()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		s.cfg, s.err = Config{}, fmt.Errorf("read %s: %w", s.path, err)
		return
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		s.cfg, s.err = Config{}, fmt.Errorf("parse %s: %w", s.path, err)
		return
	}
	if c.Enabled {
		if err := ValidateWebhookURL(c.WebhookURL); err != nil {
			s.cfg, s.err = Config{}, fmt.Errorf("invalid %s: %w", s.path, err)
			return
		}
	}
	s.cfg, s.err = c, nil
}

// Get returns the current config. A file that exists but cannot be used comes
// back as a disabled, empty config — never a stale enabled one.
func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.cfg
}

func (s *Store) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.err
}

// Save validates (when enabling) and writes c atomically (0600 — the webhook
// URL lets anyone who has it post into the user's Discord channel).
func (s *Store) Save(c Config) (Config, error) {
	if c.Enabled {
		if err := ValidateWebhookURL(c.WebhookURL); err != nil {
			return Config{}, err
		}
	}
	c.Version = 1
	c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return Config{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".alerts-*.json")
	if err != nil {
		return Config{}, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return Config{}, err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return Config{}, err
	}
	if err := tmp.Close(); err != nil {
		return Config{}, err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return Config{}, err
	}

	s.mu.Lock()
	s.init = false
	s.refreshLocked()
	s.mu.Unlock()
	return c, nil
}
