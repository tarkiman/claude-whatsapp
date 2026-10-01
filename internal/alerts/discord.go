package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// WebhookPayload is Discord's webhook message body. Content is the short line
// shown in push notifications and the mobile compact view; Embed carries the
// full, formatted detail Discord renders as a card.
//
// Reference: https://discord.com/developers/docs/resources/webhook#execute-webhook
type WebhookPayload struct {
	Username string  `json:"username,omitempty"`
	Content  string  `json:"content,omitempty"`
	Embeds   []Embed `json:"embeds,omitempty"`
}

type Embed struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	Color       int          `json:"color,omitempty"`
	Fields      []EmbedField `json:"fields,omitempty"`
	Footer      *EmbedFooter `json:"footer,omitempty"`
	Timestamp   string       `json:"timestamp,omitempty"` // RFC3339; Discord renders it localized with a relative-time hover
}

type EmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type EmbedFooter struct {
	Text string `json:"text"`
}

// Discord's own brand colors, decimal RGB — matches how Discord colors its
// own status/mention UI, so the embed looks native rather than arbitrary.
const (
	ColorRed     = 0xED4245 // down
	ColorYellow  = 0xFEE75C // degraded
	ColorGreen   = 0x57F287 // ok / recovered
	ColorBlurple = 0x5865F2 // neutral (test message)
)

const webhookUsername = "claude-whatsapp"

// Notifier posts messages to a Discord webhook. The HTTP client is
// injectable so tests never reach a real Discord endpoint.
type Notifier struct {
	HTTP *http.Client
}

func NewNotifier() *Notifier {
	return &Notifier{HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Send posts payload to webhookURL. ValidateWebhookURL already restricts
// webhookURL to Discord's own webhook shape.
func (n *Notifier) Send(ctx context.Context, webhookURL string, payload WebhookPayload) error {
	if payload.Username == "" {
		payload.Username = webhookUsername
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("discord webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("discord webhook: HTTP %d: %s", resp.StatusCode, snippet)
	}
	return nil
}

// TestPayload is what "Send test alert" posts — deliberately distinct from a
// real status alert so it's obvious, when it arrives, that it was a manual test.
func TestPayload() WebhookPayload {
	return WebhookPayload{
		Content: "🔔 Test alert from claude-whatsapp",
		Embeds: []Embed{{
			Title:       "🔔 Test alert",
			Description: "If you can see this, the webhook works. Real alerts look like this but describe an actual status change.",
			Color:       ColorBlurple,
			Footer:      &EmbedFooter{Text: webhookUsername},
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		}},
	}
}
