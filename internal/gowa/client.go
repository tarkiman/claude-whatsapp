// Package gowa is a thin REST client for a go-whatsapp-web-multidevice
// instance (https://github.com/aldinokemal/go-whatsapp-web-multidevice).
package gowa

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	user    string
	pass    string
	http    *http.Client
}

func New(baseURL, user, pass string) *Client {
	return &Client{
		baseURL: baseURL,
		user:    user,
		pass:    pass,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type apiResponse struct {
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *Client) post(path string, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()

	var out apiResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, out.Message)
	}
	return nil
}

// SendMessage sends a plain text reply to a chat.
func (c *Client) SendMessage(chatID, text string) error {
	return c.post("/send/message", map[string]string{
		"phone":   chatID,
		"message": text,
	})
}

// SetChatPresence toggles the "typing…" indicator for a chat.
// action must be "start" or "stop".
func (c *Client) SetChatPresence(chatID, action string) error {
	return c.post("/send/chat-presence", map[string]string{
		"phone":  chatID,
		"action": action,
	})
}

// React adds an emoji reaction to a message (empty emoji removes it).
func (c *Client) React(chatID, messageID, emoji string) error {
	return c.post(fmt.Sprintf("/message/%s/reaction", messageID), map[string]string{
		"phone": chatID,
		"emoji": emoji,
	})
}
