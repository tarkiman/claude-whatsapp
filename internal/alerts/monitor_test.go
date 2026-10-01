package alerts

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newTestMonitor(t *testing.T, d *fakeDiscord) (*Monitor, *time.Time) {
	t.Helper()
	m := NewMonitor(&Notifier{HTTP: d.server.Client()})
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	return m, &now
}

func enabledCfg() Config { return Config{Enabled: true, WebhookURL: "http://placeholder"} }

func TestFirstObservationAlertsImmediatelyIfAlreadyUnhealthy(t *testing.T) {
	d := newFakeDiscord(t)
	m, _ := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"gowa is unreachable"}})
	got := d.messages()
	if len(got) != 1 {
		t.Fatalf("messages = %v, want 1", got)
	}
	if !strings.Contains(got[0], "down") || !strings.Contains(got[0], "gowa is unreachable") {
		t.Errorf("message = %q", got[0])
	}
}

func TestFirstObservationHealthyStaysSilent(t *testing.T) {
	d := newFakeDiscord(t)
	m, _ := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{Overall: "ok"})
	if got := d.messages(); len(got) != 0 {
		t.Fatalf("should not alert on a healthy first observation: %v", got)
	}
}

func TestNoRepeatAlertWhileUnchanged(t *testing.T) {
	d := newFakeDiscord(t)
	m, now := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"x"}})
	for i := 0; i < 5; i++ {
		*now = now.Add(time.Minute)
		m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"x"}})
	}
	if got := d.messages(); len(got) != 1 {
		t.Fatalf("got %d messages, want exactly 1 (no spam while nothing changes)", len(got))
	}
}

func TestReminderFiresAfterThirtyMinutes(t *testing.T) {
	d := newFakeDiscord(t)
	m, now := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"x"}})
	*now = now.Add(29 * time.Minute)
	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"x"}})
	if len(d.messages()) != 1 {
		t.Fatal("reminder fired too early")
	}
	*now = now.Add(2 * time.Minute) // total 31 min
	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"x"}})
	got := d.messages()
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (initial + one reminder)", len(got))
	}
	if !strings.Contains(got[1], "ongoing") {
		t.Errorf("reminder message should say so: %q", got[1])
	}
}

func TestRecoveryIsAnnounced(t *testing.T) {
	d := newFakeDiscord(t)
	m, now := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"x"}})
	*now = now.Add(time.Minute)
	m.Check(context.Background(), cfg, Status{Overall: "ok"})
	got := d.messages()
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (down, then recovered)", len(got))
	}
	if !strings.Contains(got[1], "back to normal") {
		t.Errorf("recovery message = %q", got[1])
	}
}

func TestDegradedToDownIsItsOwnTransition(t *testing.T) {
	d := newFakeDiscord(t)
	m, now := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{Overall: "degraded", Reasons: []string{"a"}})
	*now = now.Add(time.Minute)
	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"b"}})
	got := d.messages()
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (degraded, then down)", len(got))
	}
	if !strings.Contains(got[1], "was degraded") {
		t.Errorf("transition message should mention the previous state: %q", got[1])
	}
}

func TestDisabledNeverSendsButKeepsTrackingState(t *testing.T) {
	d := newFakeDiscord(t)
	m, now := newTestMonitor(t, d)
	disabled := Config{Enabled: false, WebhookURL: d.server.URL}

	m.Check(context.Background(), disabled, Status{Overall: "down", Reasons: []string{"x"}})
	*now = now.Add(time.Minute)
	m.Check(context.Background(), disabled, Status{Overall: "ok"})
	if got := d.messages(); len(got) != 0 {
		t.Fatalf("disabled config must never send: %v", got)
	}

	// Enabling now, with the state already back to "ok", must not suddenly
	// fire a stale "recovered" message.
	*now = now.Add(time.Minute)
	m.Check(context.Background(), enabledCfgAt(d.server.URL), Status{Overall: "ok"})
	if got := d.messages(); len(got) != 0 {
		t.Fatalf("turning alerts on while already healthy must stay silent: %v", got)
	}
}

func enabledCfgAt(url string) Config { return Config{Enabled: true, WebhookURL: url} }

func TestEmptyWebhookNeverSendsEvenIfEnabled(t *testing.T) {
	d := newFakeDiscord(t)
	m, _ := newTestMonitor(t, d)
	m.Check(context.Background(), Config{Enabled: true, WebhookURL: ""}, Status{Overall: "down", Reasons: []string{"x"}})
	if got := d.messages(); len(got) != 0 {
		t.Fatalf("no webhook configured must never send: %v", got)
	}
}

func TestManyReasonsAreTruncated(t *testing.T) {
	reasons := make([]string, 20)
	for i := range reasons {
		reasons[i] = "reason"
	}
	msg := formatMessage(Status{Overall: "down", Reasons: reasons}, "ok", time.Now(), false)
	if strings.Count(msg, "• reason") != maxReasonsShown {
		t.Errorf("expected exactly %d reasons shown, got message: %q", maxReasonsShown, msg)
	}
	if !strings.Contains(msg, "• …") {
		t.Error("truncation should be indicated")
	}
}
