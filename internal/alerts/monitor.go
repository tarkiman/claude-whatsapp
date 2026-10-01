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

// Component is one part of the stack (bridge, WhatsApp, Claude) shown as its
// own field in the Discord embed, so an alert is readable without opening the
// Admin UI.
type Component struct {
	Name   string // e.g. "Bridge"
	OK     bool
	Detail string // short human text, e.g. "active", "disconnected"
}

// Status is the subset of internal/admin's StatusResponse the monitor acts
// on. Kept separate so this package has no dependency on internal/admin.
type Status struct {
	Overall    string // "ok", "degraded" or "down"
	Reasons    []string
	Components []Component
}

// Monitor watches a sequence of Status samples (normally one per minute) and
// sends a Discord message when the overall state changes, plus a periodic
// reminder while it stays unhealthy. It is in-memory only — restarting the
// admin forgets the last state, which simply means the next sample is judged
// against an assumed "ok" baseline (see Check).
type Monitor struct {
	notifier *Notifier
	now      func() time.Time

	mu             sync.Mutex
	haveBaseline   bool
	last           string
	lastSentAt     time.Time
	unhealthySince time.Time // zero while healthy; set once when "ok" is first left
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
	mustSend := changed || due

	if baseline == "ok" && st.Overall != "ok" && m.unhealthySince.IsZero() {
		m.unhealthySince = now
	}
	unhealthySince := m.unhealthySince
	if st.Overall == "ok" {
		m.unhealthySince = time.Time{} // ready to time the NEXT incident from scratch
	}

	m.last, m.haveBaseline = st.Overall, true
	if mustSend {
		m.lastSentAt = now
	}
	m.mu.Unlock()

	if !mustSend || !cfg.Enabled || cfg.WebhookURL == "" {
		return
	}
	payload := buildPayload(st, baseline, now, due, unhealthySince)
	if err := m.notifier.Send(ctx, cfg.WebhookURL, payload); err != nil {
		log.Printf("alerts: failed to send Discord notification: %v", err)
	}
}

const maxReasonsShown = 10

// formatDuration renders e.g. "2h 15m", "45m", "1d 3h" — always at least one
// unit, rounded to the minute.
func formatDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute

	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

func buildPayload(st Status, from string, at time.Time, reminder bool, unhealthySince time.Time) WebhookPayload {
	var title, content string
	var color int
	switch st.Overall {
	case "ok":
		title, content, color = "🟢 Back to normal", "🟢 claude-whatsapp is back to normal", ColorGreen
	case "down":
		title, content, color = "🔴 Down", "🔴 claude-whatsapp is down", ColorRed
	default:
		title, content, color = "🟡 Degraded", "🟡 claude-whatsapp is degraded", ColorYellow
	}

	var desc strings.Builder
	switch {
	case st.Overall == "ok" && !unhealthySince.IsZero():
		fmt.Fprintf(&desc, "Everything is back to normal after **%s** of downtime.", formatDuration(at.Sub(unhealthySince)))
	case st.Overall == "ok":
		desc.WriteString("Everything is back to normal.")
	case reminder:
		fmt.Fprintf(&desc, "Still **%s** — ongoing for **%s**.", strings.ToUpper(st.Overall), formatDuration(at.Sub(unhealthySince)))
	case from != "":
		fmt.Fprintf(&desc, "Status changed from **%s** to **%s**.", strings.ToUpper(from), strings.ToUpper(st.Overall))
	default:
		fmt.Fprintf(&desc, "Status is **%s**.", strings.ToUpper(st.Overall))
	}

	embed := Embed{
		Title:       title,
		Description: desc.String(),
		Color:       color,
		Footer:      &EmbedFooter{Text: webhookUsername + " · checked every minute"},
		Timestamp:   at.UTC().Format(time.RFC3339),
	}

	if len(st.Components) > 0 {
		for _, c := range st.Components {
			mark := "✅"
			if !c.OK {
				mark = "❌"
			}
			embed.Fields = append(embed.Fields, EmbedField{Name: c.Name, Value: mark + " " + c.Detail, Inline: true})
		}
	}

	if reasons := st.Reasons; len(reasons) > 0 {
		shown := reasons
		truncated := false
		if len(shown) > maxReasonsShown {
			shown, truncated = shown[:maxReasonsShown], true
		}
		val := "• " + strings.Join(shown, "\n• ")
		if truncated {
			val += "\n• …"
		}
		embed.Fields = append(embed.Fields, EmbedField{Name: "⚠️ Reasons", Value: val})
	}

	return WebhookPayload{Content: content, Embeds: []Embed{embed}}
}
