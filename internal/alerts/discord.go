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

// Notifier posts messages to a Discord webhook. The HTTP client is
// injectable so tests never reach a real Discord endpoint.
type Notifier struct {
	HTTP *http.Client
}

func NewNotifier() *Notifier {
	return &Notifier{HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Send posts content as the webhook's message. Discord's own webhook
// endpoint is the implicit destination; ValidateWebhookURL already restricts
// webhookURL to that shape.
func (n *Notifier) Send(ctx context.Context, webhookURL, content string) error {
	body, err := json.Marshal(map[string]string{"content": content})
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
