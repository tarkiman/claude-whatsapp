package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	// Address this bridge listens on for gowa webhooks, e.g. ":8099".
	ListenAddr string

	// Base URL of the dedicated gowa instance for this bridge, e.g. "http://localhost:3011".
	GowaBaseURL string
	GowaUser    string
	GowaPass    string

	// Must match the --webhook-secret / WHATSAPP_WEBHOOK_SECRET given to gowa.
	WebhookSecret string

	// JIDs or bare phone numbers allowed to DM this bridge directly. Anyone else is ignored.
	AllowedSenders []string

	// Group JIDs (end in "@g.us") allowed to talk to this bridge. A group
	// NOT in this list is ignored entirely, even if the individual sender
	// is in AllowedSenders — being in AllowedSenders only covers DMs.
	// Access is granted at the group level: once a group is allowlisted,
	// any member of it can trigger the bridge (matches how most group bots
	// behave — control is "did I add this bot to a group I trust", not
	// per-member). If you want tighter control, list fewer/smaller groups
	// rather than relying on per-member gating, which gowa's webhook
	// payload doesn't currently give this bridge enough data to enforce
	// (no mentioned-JIDs field — see docs/ARCHITECTURE.md).
	AllowedGroups []string

	// Path to the claude CLI binary.
	ClaudeBin string

	// Working directory claude runs in. Cross-project switching happens via
	// CLAUDE.md instructions (see ~/CLAUDE.md), same as the WhatsApp plugin setup.
	WorkDir string

	// Where the chat_id -> claude session_id mapping is persisted.
	SessionStorePath string

	// Directory holding messages that were ack'd to gowa but not yet
	// replied to — replayed on startup if the bridge died mid-flight.
	PendingDir string

	// Host-side path gowa's /app/statics is mounted at (docker-compose.yml
	// "./data/statics:/app/statics") — where the bridge resolves media
	// paths from webhook payloads like "statics/media/xxx.jpg". Relative
	// paths are relative to the bridge's working directory, same
	// convention as docker-compose.yml's own volume paths.
	MediaDir string

	// Voice-note transcription (internal/transcribe) — all optional. If
	// WhisperBin or WhisperModel don't resolve to an existing file at
	// startup, transcription is skipped and voice notes fall back to the
	// "can't listen" prompt, same as before this feature existed.
	WhisperBin   string
	WhisperModel string
	FFmpegBin    string
	WhisperLang  string
}

func FromEnv() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}

	cfg := &Config{
		ListenAddr:       getEnv("LISTEN_ADDR", ":8099"),
		GowaBaseURL:      getEnv("GOWA_BASE_URL", "http://localhost:3011"),
		GowaUser:         os.Getenv("GOWA_BASIC_AUTH_USER"),
		GowaPass:         os.Getenv("GOWA_BASIC_AUTH_PASSWORD"),
		WebhookSecret:    os.Getenv("WEBHOOK_SECRET"),
		ClaudeBin:        getEnv("CLAUDE_BIN", "claude"),
		WorkDir:          getEnv("WORK_DIR", home),
		SessionStorePath: getEnv("SESSION_STORE_PATH", home+"/.claude-whatsapp/sessions.json"),
		PendingDir:       getEnv("PENDING_DIR", home+"/.claude-whatsapp/pending"),
		MediaDir:         getEnv("GOWA_MEDIA_DIR", "./data/statics"),
		WhisperBin:       getEnv("WHISPER_BIN", "./bin/whisper-cli"),
		WhisperModel:     getEnv("WHISPER_MODEL", "./data/whisper/ggml-base.bin"),
		FFmpegBin:        getEnv("FFMPEG_BIN", "ffmpeg"),
		WhisperLang:      getEnv("WHISPER_LANG", "auto"),
	}

	if cfg.WebhookSecret == "" {
		return nil, fmt.Errorf("WEBHOOK_SECRET is required")
	}

	allowed := os.Getenv("ALLOWED_SENDERS")
	if allowed == "" {
		return nil, fmt.Errorf("ALLOWED_SENDERS is required (comma-separated JIDs/phone numbers) — refusing to run open to anyone")
	}
	cfg.AllowedSenders = splitAllowlist(allowed)
	cfg.AllowedGroups = splitAllowlist(os.Getenv("ALLOWED_GROUPS"))

	return cfg, nil
}

func splitAllowlist(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// IsGroupChat reports whether a chat_id refers to a WhatsApp group rather
// than a 1:1 DM — group JIDs always end in "@g.us".
func IsGroupChat(chatID string) bool {
	return strings.HasSuffix(chatID, "@g.us")
}

// IsAllowed decides whether a message should be processed. Groups and DMs
// are gated separately: for a group chat_id, only AllowedGroups is
// consulted (any member of an allowlisted group can trigger the bridge);
// for anything else, from/chatID is checked against AllowedSenders.
func (c *Config) IsAllowed(chatID, from string) bool {
	if IsGroupChat(chatID) {
		return matchesAllowlist(c.AllowedGroups, chatID)
	}
	return matchesAllowlist(c.AllowedSenders, from) || matchesAllowlist(c.AllowedSenders, chatID)
}

func matchesAllowlist(list []string, jid string) bool {
	for _, a := range list {
		if a == jid || strings.HasPrefix(jid, a) {
			return true
		}
	}
	return false
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
