package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFromEnvDefaultsAndRequirements(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WEBHOOK_SECRET", "s3cret")
	t.Setenv("ALLOWED_SENDERS", "6281234567890@s.whatsapp.net")
	t.Setenv("ACCESS_FILE", "")
	t.Setenv("LOG_GROUP_MESSAGES", "")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".claude-whatsapp", "access.json"); cfg.AccessFile != want {
		t.Errorf("AccessFile = %q, want %q", cfg.AccessFile, want)
	}
	if cfg.LogGroupMessages {
		t.Error("group message logging must be off by default")
	}

	t.Setenv("ALLOWED_SENDERS", "")
	if _, err := FromEnv(); err == nil {
		t.Error("an empty ALLOWED_SENDERS must be refused so a fresh install can never start open")
	}
	t.Setenv("ALLOWED_SENDERS", "6281234567890")
	t.Setenv("WEBHOOK_SECRET", "")
	if _, err := FromEnv(); err == nil {
		t.Error("an empty WEBHOOK_SECRET must be refused")
	}
}

func TestJobsConfig(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "s")
	t.Setenv("ALLOWED_SENDERS", "6281234567890")
	cfg, err := FromEnv()
	if err != nil || cfg.JobsAddr != "" || cfg.JobsWhatsAppTo != "6281234567890@s.whatsapp.net" {
		t.Fatalf("off by default: %+v, %v", cfg, err)
	}
	t.Setenv("JOBS_ADDR", "127.0.0.1:8094")
	if _, err := FromEnv(); err == nil {
		t.Error("JOBS_ADDR without a token: want an error")
	}
	t.Setenv("JOBS_TOKEN", "0123456789abcdef0123456789")
	if _, err := FromEnv(); err == nil {
		t.Error("JOBS_ADDR without JOBS_PROJECTS: want an error")
	}
	t.Setenv("JOBS_PROJECTS", "*")
	if cfg, err := FromEnv(); err != nil || cfg.JobsTimeout != 30*time.Minute || cfg.JobsMaxRunning != 2 {
		t.Errorf("valid: %+v, %v", cfg, err)
	}
	t.Setenv("JOBS_ADDR", "0.0.0.0:8094")
	if _, err := FromEnv(); err == nil {
		t.Error("JOBS_ADDR beyond loopback: want an error")
	}
}
