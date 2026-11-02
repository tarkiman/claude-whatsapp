// Package gowa is a thin REST client for a go-whatsapp-web-multidevice
// instance (https://github.com/aldinokemal/go-whatsapp-web-multidevice).
package gowa

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type Client struct {
	baseURL string
	user    string
	pass    string
	http    *http.Client

	deviceMu sync.Mutex
	deviceID string // cached, resolved lazily — see deviceIDForSend
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

// deviceIDForSend returns the device id to send as, resolving it from gowa's
// device registry on first use and caching it — gowa started requiring an
// explicit device_id on /send/* (previously it auto-selected the one
// registered device). In personal-mode's normal one-device setup this is
// resolved once and reused; post() refreshes it once if gowa still rejects it
// (e.g. after an unlink/re-pair changed the id).
func (c *Client) deviceIDForSend() (string, error) {
	c.deviceMu.Lock()
	cached := c.deviceID
	c.deviceMu.Unlock()
	if cached != "" {
		return cached, nil
	}
	return c.refreshDeviceID()
}

func (c *Client) refreshDeviceID() (string, error) {
	devs, err := c.Devices()
	if err != nil {
		return "", fmt.Errorf("resolve device id: %w", err)
	}
	if len(devs) == 0 {
		return "", fmt.Errorf("resolve device id: no WhatsApp device is linked")
	}
	// gowa's registry can hold more than one device record — e.g. leftovers
	// from earlier pairing attempts that were never completed or that got
	// logged out — and /devices' order is not meaningful (not necessarily
	// newest-first or logged-in-first). Pick the one that is ACTUALLY logged
	// in via /app/status, the one reliable check (a device record's own
	// state can say "connected" before auth completes); fall back to the
	// first entry only if none are logged in (sending will fail either way,
	// but the error should name a real device, not an empty id).
	id := devs[0].Device
	for _, d := range devs {
		if st, err := c.Status(d.Device); err == nil && st.IsLoggedIn {
			id = d.Device
			break
		}
	}
	c.deviceMu.Lock()
	c.deviceID = id
	c.deviceMu.Unlock()
	return id, nil
}

// doPost performs one send attempt with the given device id, returning
// gowa's machine-readable error code (e.g. "DEVICE_ID_REQUIRED") alongside
// any error so post() can decide whether a refreshed device id is worth a
// single retry.
func (c *Client) doPost(path string, body any, deviceID string) (string, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device-Id", deviceID)
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()

	var out apiResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)

	if resp.StatusCode >= 300 {
		return out.Code, fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, out.Message)
	}
	return "", nil
}

func (c *Client) post(path string, body any) error {
	deviceID, err := c.deviceIDForSend()
	if err != nil {
		return err
	}
	code, err := c.doPost(path, body, deviceID)
	if err != nil && code == "DEVICE_ID_REQUIRED" {
		if freshID, rerr := c.refreshDeviceID(); rerr == nil && freshID != deviceID {
			_, err = c.doPost(path, body, freshID)
		}
	}
	return err
}

// SendMessage sends a plain text reply to a chat.
func (c *Client) SendMessage(chatID, text string) error {
	return c.SendReply(chatID, text, "")
}

// SendReply sends text and, when replyToID is set, quotes that message — in a
// group this shows everyone which request the answer belongs to.
func (c *Client) SendReply(chatID, text, replyToID string) error {
	body := map[string]string{
		"phone":   chatID,
		"message": text,
	}
	if replyToID != "" {
		body["reply_message_id"] = replyToID
	}
	return c.post("/send/message", body)
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
