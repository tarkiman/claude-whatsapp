package alerts

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// reminderInterval is how often a sustained (non-"ok") state is re-announced,
// so a problem that started before anyone was watching doesn't go silent
// after the first message.
const reminderInterval = 30 * time.Minute

// Status is the subset of internal/admin's StatusResponse the monitor acts
// on. Kept separate so this package has no dependency on internal/admin.
type Status struct {
	Overall string // "ok", "degraded" or "down"
	Reasons []string
}

// Monitor watches a sequence of Status samples (normally one per minute) and
// sends a Discord message when the overall state changes, plus a periodic
// reminder while it stays unhealthy. It is in-memory only — restarting the
// admin forgets the last state, which simply means the next sample is judged
// against an assumed "ok" baseline (see Check).
type Monitor struct {
	notifier *Notifier
	now      func() time.Time

	mu           sync.Mutex
	haveBaseline bool
	last         string
	lastSentAt   time.Time
}

func NewMonitor(n *Notifier) *Monitor {
	return &Monitor{notifier: n, now: time.Now}
}

// Check evaluates one sample. Transitions and reminders are tracked even when
// alerting is disabled or misconfigured, so turning it on mid-incident does
// not immediately fire on a state it only just started observing — but a
// genuine change is still sent the moment it is both observed and enabled.
func (m *Monitor) Check(ctx context.Context, cfg Config, st Status) {
	now := m.now()

	m.mu.Lock()
	baseline := m.last
	if !m.haveBaseline {
		baseline = "ok" // first observation: alert immediately if already unhealthy
	}
	changed := st.Overall != baseline
	due := !changed && st.Overall != "ok" && m.haveBaseline && now.Sub(m.lastSentAt) >= reminderInterval
	m.last, m.haveBaseline = st.Overall, true
	if changed || due {
		m.lastSentAt = now
	}
	mustSend := changed || due
	m.mu.Unlock()

	if !mustSend || !cfg.Enabled || cfg.WebhookURL == "" {
		return
	}
	msg := formatMessage(st, baseline, now, due)
	if err := m.notifier.Send(ctx, cfg.WebhookURL, msg); err != nil {
		log.Printf("alerts: failed to send Discord notification: %v", err)
	}
}

const maxReasonsShown = 10

func formatMessage(st Status, from string, at time.Time, reminder bool) string {
	var b strings.Builder
	switch {
	case st.Overall == "ok":
		b.WriteString("🟢 **claude-whatsapp is back to normal**")
	case st.Overall == "down":
		b.WriteString("🔴 **claude-whatsapp is down**")
	default:
		b.WriteString("🟡 **claude-whatsapp is degraded**")
	}
	if reminder {
		b.WriteString(" (still ongoing)")
	} else if from != "" && st.Overall != "ok" {
		fmt.Fprintf(&b, " (was %s)", from)
	}
	reasons := st.Reasons
	truncated := false
	if len(reasons) > maxReasonsShown {
		truncated = true
		reasons = reasons[:maxReasonsShown]
	}
	for _, r := range reasons {
		fmt.Fprintf(&b, "\n• %s", r)
	}
	if truncated {
		b.WriteString("\n• …")
	}
	fmt.Fprintf(&b, "\n_%s_", at.UTC().Format("2006-01-02 15:04:05 UTC"))
	return b.String()
}
