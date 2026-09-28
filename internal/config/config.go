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

	// ALLOWED_SENDERS seeds the access policy until the Admin UI writes an
	// access file (see internal/access): the first number becomes the single
	// personal-mode owner. Required, so a fresh install can never start open.
	AllowedSenders []string

	// Deprecated: groups are configured as "team mode" in the Admin UI now
	// (one group, an approved-member roster, @mention required). A non-empty
	// ALLOWED_GROUPS no longer admits anyone; the bridge only warns about it.
	AllowedGroups []string

	// Where the access policy (personal/team mode, roster) is stored. Written
	// by the Admin UI, re-read by the bridge whenever it changes.
	AccessFile string

	// LOG_GROUP_MESSAGES=1 logs sender, ids and text of group messages (with
	// the decision taken) to help diagnose @mention detection. Off by default
	// because it writes message content to the log.
	LogGroupMessages bool

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
		AccessFile:       getEnv("ACCESS_FILE", home+"/.claude-whatsapp/access.json"),
		LogGroupMessages: os.Getenv("LOG_GROUP_MESSAGES") == "1",
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

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
