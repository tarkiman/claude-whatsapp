package config

import (
	"path/filepath"
	"testing"
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
