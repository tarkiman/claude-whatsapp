package alerts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	validHook   = "https://discord.com/api/webhooks/123456789012345678/AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abc"
	validHook2  = "https://discordapp.com/api/webhooks/987654321098765432/ZzYyXxWwVvUuTtSsRrQqPpOoNnMm01234567"
	badHostHook = "https://evil.example/api/webhooks/123456789012345678/AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
)

func TestValidateWebhookURL(t *testing.T) {
	for _, ok := range []string{validHook, validHook2, validHook + "/slack"} {
		if err := ValidateWebhookURL(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for name, bad := range map[string]string{
		"empty":            "",
		"not a url":        "not a url at all",
		"wrong host":       badHostHook,
		"http not https":   strings.Replace(validHook, "https://", "http://", 1),
		"missing token":    "https://discord.com/api/webhooks/123456789012345678/",
		"short token":      "https://discord.com/api/webhooks/123456789012345678/short",
		"non-numeric id":   "https://discord.com/api/webhooks/not-a-number/AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
		"trailing garbage": validHook + "?evil=1",
	} {
		if err := ValidateWebhookURL(bad); err == nil {
			t.Errorf("%s (%q) should be rejected", name, bad)
		}
	}
}

func TestMaskedNeverRevealsTheToken(t *testing.T) {
	m := Masked(validHook)
	if strings.Contains(m, "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abc") {
		t.Fatalf("masked value leaks the token: %q", m)
	}
	if !strings.HasPrefix(m, "https://discord.com/api/webhooks/123456789012345678/") {
		t.Errorf("masked value should still show the host and id: %q", m)
	}
	if Masked("not a webhook") != "" {
		t.Error("Masked of an invalid URL should be empty, not guess")
	}
}

func TestStoreSaveValidatesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "alerts.json")
	st := Open(path)

	if c := st.Get(); c.Enabled || c.WebhookURL != "" {
		t.Fatalf("before any file exists: %+v", c)
	}

	if _, err := st.Save(Config{Enabled: true, WebhookURL: "not a webhook"}); err == nil {
		t.Fatal("an invalid webhook must be refused when enabling")
	}
	if _, err := st.Save(Config{Enabled: false, WebhookURL: "not a webhook either"}); err != nil {
		t.Fatalf("a disabled config should not need a valid webhook: %v", err)
	}

	saved, err := st.Save(Config{Enabled: true, WebhookURL: validHook})
	if err != nil {
		t.Fatal(err)
	}
	if saved.UpdatedAt == "" {
		t.Error("UpdatedAt not set")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("alerts file mode = %v, want 0600", fi.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), validHook) {
		t.Error("the webhook was not actually written")
	}

	other := Open(path)
	if c := other.Get(); !c.Enabled || c.WebhookURL != validHook {
		t.Fatalf("a second store on the same file should see it: %+v", c)
	}
}

func TestCorruptOrInvalidFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	for name, content := range map[string]string{
		"not json":                 "{ nope",
		"enabled with bad webhook": `{"enabled":true,"webhookUrl":"not a webhook"}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		st := Open(path)
		c := st.Get()
		if c.Enabled {
			t.Errorf("%s: a broken file must never be treated as enabled: %+v", name, c)
		}
		if st.Err() == nil {
			t.Errorf("%s: Err() should explain the problem", name)
		}
	}
}

func TestHotReloadPicksUpAnEditMadeElsewhere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	st := Open(path)
	if _, err := st.Save(Config{Enabled: true, WebhookURL: validHook}); err != nil {
		t.Fatal(err)
	}
	if !st.Get().Enabled {
		t.Fatal("expected enabled")
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := st.Save(Config{Enabled: false, WebhookURL: validHook}); err != nil {
		t.Fatal(err)
	}
	if st.Get().Enabled {
		t.Fatal("did not pick up the disable")
	}
}
