package config

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"
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

	// Where the Admin UI keeps its login (username + password hash, 0600).
	AdminAuthFile string

	// Where the Discord alert configuration (webhook URL, on/off) is stored.
	AlertsFile string

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

	// Voice jobs (internal/jobs): a localhost API that kawan-bridge (Eca's voice
	// assistant) calls to run Claude Code tasks in the background. Off unless
	// JOBS_ADDR is set; JOBS_TOKEN is then required.
	JobsAddr          string
	JobsToken         string
	JobsProjectsDir   string        // projects are the git repos directly in here
	JobsProjects      []string      // allowed project names, or "*" for all of them
	JobsNotifyURL     string        // spoken reports go here (kawan-bridge /notify)
	JobsWhatsAppTo    string        // full reports go to this chat; default: the ALLOWED_SENDERS owner
	JobsSessionsPath  string        // project -> session, separate from the WhatsApp chats' sessions
	JobsTimeout       time.Duration // per job
	JobsMaxRunning    int           // across all projects
	JobsProgressEvery time.Duration // spoken progress note interval while a job runs (0 = off)
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
		AdminAuthFile:    getEnv("ADMIN_AUTH_FILE", home+"/.claude-whatsapp/admin.json"),
		AlertsFile:       getEnv("ALERTS_FILE", home+"/.claude-whatsapp/alerts.json"),
		LogGroupMessages: os.Getenv("LOG_GROUP_MESSAGES") == "1",
		PendingDir:       getEnv("PENDING_DIR", home+"/.claude-whatsapp/pending"),
		MediaDir:         getEnv("GOWA_MEDIA_DIR", "./data/statics"),
		WhisperBin:       getEnv("WHISPER_BIN", "./bin/whisper-cli"),
		WhisperModel:     getEnv("WHISPER_MODEL", "./data/whisper/ggml-base.bin"),
		FFmpegBin:        getEnv("FFMPEG_BIN", "ffmpeg"),
		WhisperLang:      getEnv("WHISPER_LANG", "auto"),
	}

	if err := cfg.jobsFromEnv(home); err != nil {
		return nil, err
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
	if cfg.JobsWhatsAppTo == "" && len(cfg.AllowedSenders) > 0 {
		cfg.JobsWhatsAppTo = cfg.AllowedSenders[0]
	}
	if cfg.JobsWhatsAppTo != "" && !strings.Contains(cfg.JobsWhatsAppTo, "@") {
		cfg.JobsWhatsAppTo += "@s.whatsapp.net"
	}

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

func (cfg *Config) jobsFromEnv(home string) error {
	cfg.JobsAddr = os.Getenv("JOBS_ADDR")
	cfg.JobsToken = os.Getenv("JOBS_TOKEN")
	cfg.JobsProjectsDir = getEnv("JOBS_PROJECTS_DIR", home+"/repository")
	cfg.JobsProjects = splitAllowlist(os.Getenv("JOBS_PROJECTS"))
	cfg.JobsNotifyURL = os.Getenv("JOBS_NOTIFY_URL")
	cfg.JobsWhatsAppTo = os.Getenv("JOBS_WHATSAPP_TO")
	cfg.JobsSessionsPath = getEnv("JOBS_SESSIONS_PATH", home+"/.claude-whatsapp/voice-sessions.json")
	var err error
	if cfg.JobsTimeout, err = time.ParseDuration(getEnv("JOBS_TIMEOUT", "30m")); err != nil {
		return fmt.Errorf("JOBS_TIMEOUT: %w", err)
	}
	if cfg.JobsProgressEvery, err = time.ParseDuration(getEnv("JOBS_PROGRESS_EVERY", "4m")); err != nil {
		return fmt.Errorf("JOBS_PROGRESS_EVERY: %w", err)
	}
	if _, err := fmt.Sscan(getEnv("JOBS_MAX_RUNNING", "2"), &cfg.JobsMaxRunning); err != nil || cfg.JobsMaxRunning < 1 {
		return fmt.Errorf("JOBS_MAX_RUNNING: expected a number >= 1")
	}
	if cfg.JobsAddr == "" {
		return nil
	}
	if len(cfg.JobsToken) < 24 {
		return fmt.Errorf("JOBS_TOKEN is required (24+ characters) when JOBS_ADDR is set")
	}
	if len(cfg.JobsProjects) == 0 {
		return fmt.Errorf("JOBS_PROJECTS is required when JOBS_ADDR is set (comma-separated repo names, or *)")
	}
	host, _, err := net.SplitHostPort(cfg.JobsAddr)
	if err != nil {
		return fmt.Errorf("JOBS_ADDR: %w", err)
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return fmt.Errorf("JOBS_ADDR must be a loopback address (it can run code in your repos), got %q", cfg.JobsAddr)
	}
	return nil
}
