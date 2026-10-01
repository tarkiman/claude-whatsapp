package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/alerts"
)

// Discord alerting: the "Alerts" card. Checking the stack's status and
// deciding whether to notify lives in internal/alerts; this file only wires
// it to the Admin UI and runs it on a timer.

const alertCheckInterval = time.Minute

type alertsView struct {
	Enabled     bool   `json:"enabled"`
	WebhookMask string `json:"webhookMask,omitempty"`
	Configured  bool   `json:"configured"`
	Error       string `json:"error,omitempty"`
}

func (s *Server) alertsView() alertsView {
	cfg := s.alerts.Get()
	v := alertsView{Enabled: cfg.Enabled, Configured: cfg.WebhookURL != ""}
	if cfg.WebhookURL != "" {
		v.WebhookMask = alerts.Masked(cfg.WebhookURL)
	}
	if err := s.alerts.Err(); err != nil {
		v.Error = err.Error()
	}
	return v
}

func (s *Server) handleAlertsGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.alertsView())
}

type saveAlertsRequest struct {
	Enabled    bool    `json:"enabled"`
	WebhookURL *string `json:"webhookUrl"` // nil = keep the one already stored
}

func (s *Server) handleAlertsSave(w http.ResponseWriter, r *http.Request) {
	var req saveAlertsRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	cfg := s.alerts.Get()
	cfg.Enabled = req.Enabled
	if req.WebhookURL != nil {
		cfg.WebhookURL = *req.WebhookURL
	}
	if cfg.Enabled && cfg.WebhookURL == "" {
		writeErr(w, http.StatusBadRequest, errors.New("paste a Discord webhook URL before enabling alerts"))
		return
	}
	if _, err := s.alerts.Save(cfg); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.alertsView())
}

// handleAlertsTest sends one real message so the operator can confirm the
// webhook actually works, independent of whether alerting is enabled or the
// stack is currently unhealthy.
func (s *Server) handleAlertsTest(w http.ResponseWriter, r *http.Request) {
	cfg := s.alerts.Get()
	if cfg.WebhookURL == "" {
		writeErr(w, http.StatusBadRequest, errors.New("no webhook URL saved yet"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	msg := "🔔 Test alert from claude-whatsapp's Admin UI — if you can see this, the webhook works."
	if err := s.notifier.Send(ctx, cfg.WebhookURL, msg); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "test message sent"})
}

// RunAlertLoop runs forever (or until ctx is done) checking the stack's
// status once a minute and alerting on change via internal/alerts.
func (s *Server) RunAlertLoop(ctx context.Context) {
	t := time.NewTicker(alertCheckInterval)
	defer t.Stop()
	for {
		s.alertCycle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) alertCycle(ctx context.Context) {
	st := s.computeStatus(ctx)
	s.alertMon.Check(ctx, s.alerts.Get(), alerts.Status{Overall: st.Overall, Reasons: st.Reasons})
}
