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

// desc is the embed description of the n-th message sent (0-indexed) — where
// the human-readable "what happened" text lives, as opposed to Content
// (the short push-notification line).
func desc(t *testing.T, d *fakeDiscord, n int) string {
	t.Helper()
	sent := d.sent()
	if n >= len(sent) || len(sent[n].Embeds) == 0 {
		t.Fatalf("no embed at index %d in %+v", n, sent)
	}
	return sent[n].Embeds[0].Description
}

func TestFirstObservationAlertsImmediatelyIfAlreadyUnhealthy(t *testing.T) {
	d := newFakeDiscord(t)
	m, _ := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"gowa is unreachable"}})
	got := d.sent()
	if len(got) != 1 {
		t.Fatalf("messages = %v, want 1", got)
	}
	if !strings.Contains(got[0].Content, "down") {
		t.Errorf("content = %q", got[0].Content)
	}
	if len(got[0].Embeds) != 1 || len(got[0].Embeds[0].Fields) != 1 || !strings.Contains(got[0].Embeds[0].Fields[0].Value, "gowa is unreachable") {
		t.Errorf("embed = %+v", got[0].Embeds)
	}
	if got[0].Embeds[0].Color != ColorRed {
		t.Errorf("color = %#x, want red", got[0].Embeds[0].Color)
	}
}

func TestFirstObservationHealthyStaysSilent(t *testing.T) {
	d := newFakeDiscord(t)
	m, _ := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{Overall: "ok"})
	if got := d.sent(); len(got) != 0 {
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
	if got := d.sent(); len(got) != 1 {
		t.Fatalf("got %d messages, want exactly 1 (no spam while nothing changes)", len(got))
	}
}

func TestReminderFiresAfterThirtyMinutesWithTheRealDuration(t *testing.T) {
	d := newFakeDiscord(t)
	m, now := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"x"}})
	*now = now.Add(29 * time.Minute)
	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"x"}})
	if len(d.sent()) != 1 {
		t.Fatal("reminder fired too early")
	}
	*now = now.Add(2 * time.Minute) // total 31 min
	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"x"}})
	got := d.sent()
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (initial + one reminder)", len(got))
	}
	d1 := desc(t, d, 1)
	if !strings.Contains(d1, "ongoing") || !strings.Contains(d1, "31m") {
		t.Errorf("reminder description should state the real elapsed time: %q", d1)
	}
}

func TestRecoveryAnnouncesTheTotalDowntime(t *testing.T) {
	d := newFakeDiscord(t)
	m, now := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"x"}})
	*now = now.Add(90 * time.Minute)
	m.Check(context.Background(), cfg, Status{Overall: "ok"})
	got := d.sent()
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (down, then recovered)", len(got))
	}
	if !strings.Contains(got[1].Content, "back to normal") {
		t.Errorf("recovery content = %q", got[1].Content)
	}
	d1 := desc(t, d, 1)
	if !strings.Contains(d1, "1h 30m") {
		t.Errorf("recovery description should state total downtime: %q", d1)
	}
	if got[1].Embeds[0].Color != ColorGreen {
		t.Errorf("color = %#x, want green", got[1].Embeds[0].Color)
	}
}

func TestDowntimeSurvivesIntermediateSeverityChanges(t *testing.T) {
	// degraded -> down -> degraded -> ok: the recovery duration must count
	// from the FIRST departure from "ok", not from the last severity change.
	d := newFakeDiscord(t)
	m, now := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{Overall: "degraded", Reasons: []string{"a"}})
	*now = now.Add(10 * time.Minute)
	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"b"}})
	*now = now.Add(20 * time.Minute)
	m.Check(context.Background(), cfg, Status{Overall: "degraded", Reasons: []string{"a"}})
	*now = now.Add(5 * time.Minute)
	m.Check(context.Background(), cfg, Status{Overall: "ok"})

	got := d.sent()
	if len(got) != 4 {
		t.Fatalf("got %d messages, want 4 transitions", len(got))
	}
	final := desc(t, d, 3)
	if !strings.Contains(final, "35m") { // 10 + 20 + 5, not just the last 5m leg
		t.Errorf("recovery description = %q, want the full 35m outage", final)
	}
}

func TestUnhealthySinceResetsAfterRecoveryForTheNextIncident(t *testing.T) {
	// A second, later incident must report its OWN duration, not one
	// inflated by a stale start time left over from the first incident.
	d := newFakeDiscord(t)
	m, now := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"x"}})
	*now = now.Add(2 * time.Hour)
	m.Check(context.Background(), cfg, Status{Overall: "ok"}) // first incident: 2h

	*now = now.Add(24 * time.Hour) // a full day of healthy operation
	m.Check(context.Background(), cfg, Status{Overall: "down", Reasons: []string{"y"}})
	*now = now.Add(5 * time.Minute)
	m.Check(context.Background(), cfg, Status{Overall: "ok"}) // second incident: 5m

	got := d.sent()
	if len(got) != 4 {
		t.Fatalf("got %d messages, want 4", len(got))
	}
	final := desc(t, d, 3)
	if !strings.Contains(final, "5m") || strings.Contains(final, "24h") || strings.Contains(final, "1d") {
		t.Errorf("second recovery should report only its own 5m, not the gap since the first incident: %q", final)
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
	got := d.sent()
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (degraded, then down)", len(got))
	}
	d1 := desc(t, d, 1)
	if !strings.Contains(d1, "DEGRADED") || !strings.Contains(d1, "DOWN") {
		t.Errorf("transition description should mention both states: %q", d1)
	}
}

func TestComponentsBecomeEmbedFields(t *testing.T) {
	d := newFakeDiscord(t)
	m, _ := newTestMonitor(t, d)
	cfg := enabledCfg()
	cfg.WebhookURL = d.server.URL

	m.Check(context.Background(), cfg, Status{
		Overall: "down",
		Reasons: []string{"WhatsApp is logged out"},
		Components: []Component{
			{Name: "🌉 Bridge", OK: true, Detail: "active"},
			{Name: "📱 WhatsApp", OK: false, Detail: "logged out"},
			{Name: "🤖 Claude", OK: true, Detail: "signed in"},
		},
	})
	got := d.sent()
	if len(got) != 1 {
		t.Fatal("expected one alert")
	}
	fields := got[0].Embeds[0].Fields
	if len(fields) != 4 { // 3 components + the reasons field
		t.Fatalf("fields = %+v", fields)
	}
	if fields[0].Name != "🌉 Bridge" || fields[0].Value != "✅ active" || !fields[0].Inline {
		t.Errorf("bridge field = %+v", fields[0])
	}
	if fields[1].Value != "❌ logged out" {
		t.Errorf("whatsapp field = %+v", fields[1])
	}
	if fields[3].Name != "⚠️ Reasons" {
		t.Errorf("reasons field = %+v", fields[3])
	}
	if fields[3].Inline {
		t.Error("the reasons field should span the full width, not sit inline")
	}
}

func TestDisabledNeverSendsButKeepsTrackingState(t *testing.T) {
	d := newFakeDiscord(t)
	m, now := newTestMonitor(t, d)
	disabled := Config{Enabled: false, WebhookURL: d.server.URL}

	m.Check(context.Background(), disabled, Status{Overall: "down", Reasons: []string{"x"}})
	*now = now.Add(time.Minute)
	m.Check(context.Background(), disabled, Status{Overall: "ok"})
	if got := d.sent(); len(got) != 0 {
		t.Fatalf("disabled config must never send: %v", got)
	}

	// Enabling now, with the state already back to "ok", must not suddenly
	// fire a stale "recovered" message.
	*now = now.Add(time.Minute)
	m.Check(context.Background(), enabledCfgAt(d.server.URL), Status{Overall: "ok"})
	if got := d.sent(); len(got) != 0 {
		t.Fatalf("turning alerts on while already healthy must stay silent: %v", got)
	}
}

func enabledCfgAt(url string) Config { return Config{Enabled: true, WebhookURL: url} }

func TestEmptyWebhookNeverSendsEvenIfEnabled(t *testing.T) {
	d := newFakeDiscord(t)
	m, _ := newTestMonitor(t, d)
	m.Check(context.Background(), Config{Enabled: true, WebhookURL: ""}, Status{Overall: "down", Reasons: []string{"x"}})
	if got := d.sent(); len(got) != 0 {
		t.Fatalf("no webhook configured must never send: %v", got)
	}
}

func TestManyReasonsAreTruncated(t *testing.T) {
	reasons := make([]string, 20)
	for i := range reasons {
		reasons[i] = "reason"
	}
	payload := buildPayload(Status{Overall: "down", Reasons: reasons}, "ok", time.Now(), false, time.Now())
	field := payload.Embeds[0].Fields[0]
	if strings.Count(field.Value, "• reason") != maxReasonsShown {
		t.Errorf("expected exactly %d reasons shown, got: %q", maxReasonsShown, field.Value)
	}
	if !strings.Contains(field.Value, "• …") {
		t.Error("truncation should be indicated")
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		20 * time.Second: "0m",
		90 * time.Second: "2m",
		45 * time.Minute: "45m",
		90 * time.Minute: "1h 30m",
		25 * time.Hour:   "1d 1h",
		3*24*time.Hour + 2*time.Hour + 5*time.Minute: "3d 2h",
	}
	for d, want := range cases {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
