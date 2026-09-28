// Package access decides who may give Claude instructions over WhatsApp.
//
// There are exactly two modes:
//
//   - personal: one phone number may DM the bot; every group is ignored.
//   - team:     one WhatsApp group; only members explicitly approved on the
//     roster may instruct the bot, and only in messages that @mention it.
//     Members who are added to the group later are NOT approved until
//     someone ticks them in the Admin UI.
//
// The policy lives in a small JSON file (default ~/.claude-whatsapp/access.json)
// written by the Admin UI and re-read by the bridge when it changes, so no
// restart is needed. Anything unclear fails closed.
package access

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
	"unicode"
)

type Mode string

const (
	ModePersonal Mode = "personal"
	ModeTeam     Mode = "team"
)

const (
	phoneServer = "s.whatsapp.net"
	groupServer = "g.us"
	lidServer   = "lid"

	maxNameLen = 60
)

// Member is one approved person in team mode.
type Member struct {
	Phone string `json:"phone"`         // digits only, with country code
	LID   string `json:"lid,omitempty"` // digits only; WhatsApp's alternate identifier
	Name  string `json:"name,omitempty"`
}

type Personal struct {
	Number string `json:"number"`
}

type Team struct {
	Group   string   `json:"group"` // ...@g.us
	Name    string   `json:"name,omitempty"`
	Members []Member `json:"members"`
}

type Config struct {
	Version   int      `json:"version"`
	Mode      Mode     `json:"mode"`
	Personal  Personal `json:"personal"`
	Team      Team     `json:"team"`
	UpdatedAt string   `json:"updatedAt,omitempty"`
}

// --- JID helpers -------------------------------------------------------------

// SplitJID splits a WhatsApp JID into user and server, dropping the ":device"
// suffix multi-device JIDs carry ("6281…:12@s.whatsapp.net" is the same person
// as "6281…@s.whatsapp.net").
func SplitJID(jid string) (user, server string) {
	user, server, _ = strings.Cut(jid, "@")
	user, _, _ = strings.Cut(user, ":")
	return user, server
}

// NormalizeNumber returns the digits of a phone number written in any common
// form ("+62 812-3456-7890", "6281…@s.whatsapp.net").
func NormalizeNumber(s string) (string, error) {
	s = strings.TrimSpace(s)
	if u, _, ok := strings.Cut(s, "@"); ok {
		s = u
	}
	s, _, _ = strings.Cut(s, ":")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", fmt.Errorf("%q is not a phone number", s)
		}
	}
	d := b.String()
	switch {
	case d == "":
		return "", errors.New("phone number is empty")
	case strings.HasPrefix(d, "0"):
		return "", fmt.Errorf("%q starts with 0 — use the country code (e.g. 62812… not 0812…)", s)
	case len(d) < 8 || len(d) > 15:
		return "", fmt.Errorf("%q has %d digits — include the country code (8–15 digits)", s, len(d))
	}
	return d, nil
}

// NormalizeLID returns the digits of an "@lid" identifier ("" if none).
func NormalizeLID(s string) string {
	u, srv := SplitJID(strings.TrimSpace(s))
	if srv != "" && srv != lidServer {
		return ""
	}
	for _, r := range u {
		if !unicode.IsDigit(r) {
			return ""
		}
	}
	return u
}

func CleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxNameLen {
		s = string(r[:maxNameLen])
	}
	return s
}

func IsGroupJID(jid string) bool {
	u, s := SplitJID(jid)
	return u != "" && s == groupServer
}

// --- validation ---------------------------------------------------------------

// Normalize validates the config and rewrites it into canonical form. The
// section of the mode that is NOT active is kept (best effort) so switching
// back and forth doesn't lose the roster, but only the active one is checked.
func (c *Config) Normalize() error {
	c.Version = 1
	if n, err := NormalizeNumber(c.Personal.Number); err == nil {
		c.Personal.Number = n
	} else if c.Mode == ModePersonal {
		return fmt.Errorf("personal number: %w", err)
	} else {
		c.Personal.Number = ""
	}

	c.Team.Name = CleanName(c.Team.Name)
	if c.Team.Group != "" {
		if !IsGroupJID(c.Team.Group) {
			if c.Mode == ModeTeam {
				return fmt.Errorf("team group %q is not a group JID (…@g.us)", c.Team.Group)
			}
			c.Team.Group = ""
		} else {
			u, _ := SplitJID(c.Team.Group)
			c.Team.Group = u + "@" + groupServer
		}
	}
	seen := map[string]bool{}
	members := make([]Member, 0, len(c.Team.Members))
	for _, m := range c.Team.Members {
		p, err := NormalizeNumber(m.Phone)
		if err != nil {
			return fmt.Errorf("team member %q: %w", m.Name, err)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		members = append(members, Member{Phone: p, LID: NormalizeLID(m.LID), Name: CleanName(m.Name)})
	}
	c.Team.Members = members

	switch c.Mode {
	case ModePersonal:
	case ModeTeam:
		if c.Team.Group == "" {
			return errors.New("team mode needs a group")
		}
	default:
		return fmt.Errorf("unknown mode %q (personal or team)", c.Mode)
	}
	return nil
}

// Legacy builds a personal-mode config from the ALLOWED_SENDERS environment
// variable, used until the Admin UI writes an access file. Personal mode has
// exactly one number: the first valid entry wins and the rest are returned as
// ignored.
func Legacy(senders []string) (cfg Config, ignored []string) {
	cfg = Config{Version: 1, Mode: ModePersonal}
	for _, s := range senders {
		n, err := NormalizeNumber(s)
		if err != nil {
			ignored = append(ignored, s)
			continue
		}
		if cfg.Personal.Number == "" {
			cfg.Personal.Number = n
		} else if n != cfg.Personal.Number {
			ignored = append(ignored, s)
		}
	}
	return cfg, ignored
}

// --- decisions ----------------------------------------------------------------

// Incoming is the part of a webhook the policy looks at.
type Incoming struct {
	ChatID   string
	From     string
	FromLID  string
	Body     string
	HasMedia bool
}

// Bot is the bot account's own identity (digits only).
type Bot struct {
	Phone string
	LID   string
}

type Decision struct {
	Allow  bool
	Reason string // machine-readable code, for logs
	Quiet  bool   // a deny that is normal chatter and should not be logged
	Team   bool   // the message came through team mode
	Body   string // message text with the bot's own @mention removed
}

func phoneMatches(jid, number string) bool {
	u, s := SplitJID(jid)
	return number != "" && s == phoneServer && u == number
}

func (m Member) matches(in Incoming) bool {
	if phoneMatches(in.From, m.Phone) {
		return true
	}
	if m.LID == "" {
		return false
	}
	for _, j := range []string{in.From, in.FromLID} {
		u, s := SplitJID(j)
		if (s == lidServer || (j == in.FromLID && s == "")) && u == m.LID {
			return true
		}
	}
	return false
}

// Authorize checks WHO and WHERE — everything except the @mention. It is also
// what the pending-queue replay uses, so a queued message from someone who has
// since been removed is dropped instead of executed.
func (c Config) Authorize(in Incoming) Decision {
	switch c.Mode {
	case ModePersonal:
		if IsGroupJID(in.ChatID) {
			return Decision{Reason: "personal:groups_disabled", Quiet: true}
		}
		if phoneMatches(in.From, c.Personal.Number) || phoneMatches(in.ChatID, c.Personal.Number) {
			return Decision{Allow: true, Body: in.Body}
		}
		return Decision{Reason: "personal:not_owner"}

	case ModeTeam:
		if !IsGroupJID(in.ChatID) {
			return Decision{Reason: "team:dm_ignored", Quiet: true, Team: true}
		}
		u, _ := SplitJID(in.ChatID)
		gu, _ := SplitJID(c.Team.Group)
		if u == "" || u != gu {
			return Decision{Reason: "team:other_group", Quiet: true, Team: true}
		}
		for _, m := range c.Team.Members {
			if m.matches(in) {
				return Decision{Allow: true, Team: true, Body: in.Body}
			}
		}
		return Decision{Reason: "team:not_approved", Team: true}
	}
	return Decision{Reason: "no_policy"}
}

var mentionRe = regexp.MustCompile(`@(\d+)`)

// Mentioned reports whether body @mentions the bot, and returns body with those
// mentions removed. Matching is exact on the whole number: "@628950000000"
// does not mention the bot "6289500000001".
func Mentioned(body string, bot Bot) (bool, string) {
	found := false
	cleaned := mentionRe.ReplaceAllStringFunc(body, func(tok string) string {
		n := tok[1:]
		if (bot.Phone != "" && n == bot.Phone) || (bot.LID != "" && n == bot.LID) {
			found = true
			return ""
		}
		return tok
	})
	return found, strings.Join(strings.Fields(cleaned), " ")
}

// HasUnknownMention reports whether body contains any @number token, which is
// when it is worth asking gowa for the bot's LID.
func HasUnknownMention(body string) bool { return mentionRe.MatchString(body) }

// Decide is the complete gate for a live message.
func (c Config) Decide(in Incoming, bot Bot) Decision {
	d := c.Authorize(in)
	if c.Mode != ModeTeam || !IsGroupJID(in.ChatID) {
		return d
	}
	// In a team group only messages that @mention the bot count; everything
	// else is ordinary conversation between people and must stay untouched.
	mentioned, cleaned := Mentioned(in.Body, bot)
	if !mentioned {
		return Decision{Reason: "team:not_mentioned", Quiet: true, Team: true}
	}
	if !d.Allow {
		return d
	}
	if cleaned == "" && !in.HasMedia {
		return Decision{Reason: "team:empty", Quiet: true, Team: true}
	}
	d.Body = cleaned
	return d
}

// --- storage ------------------------------------------------------------------

// Store holds the current policy and keeps it in sync with the file on disk.
type Store struct {
	path     string
	fallback Config

	mu    sync.Mutex
	cfg   Config
	src   string
	mtime time.Time
	err   error
	init  bool
}

// Open returns a Store for path. While the file does not exist, fallback (the
// policy derived from the environment) is in force.
func Open(path string, fallback Config) *Store {
	return &Store{path: path, fallback: fallback}
}

func (s *Store) Path() string { return s.path }

// Get returns the current policy, reloading the file if it changed. A file
// that exists but cannot be read or validated yields an empty policy that
// denies everything — never a silently more permissive one.
func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.cfg
}

// Source reports where the policy in force comes from: "file" or "env".
func (s *Store) Source() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.src
}

// Err is the last problem reading the file (nil when healthy).
func (s *Store) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.err
}

func (s *Store) refreshLocked() {
	fi, err := os.Stat(s.path)
	if err != nil {
		if s.init && s.src == "env" {
			return
		}
		s.cfg, s.src, s.err, s.init = s.fallback, "env", nil, true
		s.mtime = time.Time{}
		return
	}
	if s.init && s.src == "file" && fi.ModTime().Equal(s.mtime) && s.err == nil {
		return
	}
	s.init, s.src, s.mtime = true, "file", fi.ModTime()

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
	if err := c.Normalize(); err != nil {
		s.cfg, s.err = Config{}, fmt.Errorf("invalid %s: %w", s.path, err)
		return
	}
	s.cfg, s.err = c, nil
}

// Save validates c and writes it atomically (0600).
func (s *Store) Save(c Config) (Config, error) {
	if err := c.Normalize(); err != nil {
		return Config{}, err
	}
	c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return Config{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".access-*.json")
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
