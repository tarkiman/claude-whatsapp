package gowa

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Device struct {
	Name   string `json:"name"`
	Device string `json:"device"`
	JID    string `json:"jid"`
}

type ConnStatus struct {
	DeviceID    string `json:"device_id"`
	IsConnected bool   `json:"is_connected"`
	IsLoggedIn  bool   `json:"is_logged_in"`
	JID         string `json:"jid"`
}

type LoginQR struct {
	QRLink     string `json:"qr_link"`
	QRDuration int    `json:"qr_duration"`
}

type PairCode struct {
	PairCode string `json:"pair_code"`
}

// call performs a request against gowa and returns the "results" field of its
// envelope. gowa reports failures as non-2xx with a message, which is surfaced
// verbatim so the admin UI can show it.
func (c *Client) call(method, path string, q url.Values, body io.Reader) (json.RawMessage, error) {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()

	var env struct {
		Message string          `json:"message"`
		Results json.RawMessage `json:"results"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode >= 300 {
		msg := env.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return nil, fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, msg)
	}
	return env.Results, nil
}

func deviceQuery(deviceID string) url.Values {
	q := url.Values{}
	if deviceID != "" {
		q.Set("device_id", deviceID)
	}
	return q
}

// Devices lists the device registry via GET /devices. /app/devices is not
// used because it answers 400 DEVICE_ID_REQUIRED when the registry is empty
// (e.g. right after a logout), which is a normal state to recover from. An
// empty registry comes back as `"results": null`.
func (c *Client) Devices() ([]Device, error) {
	res, err := c.call(http.MethodGet, "/devices", nil, nil)
	if err != nil {
		return nil, err
	}
	if len(res) == 0 || string(res) == "null" {
		return nil, nil
	}
	var raw []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		JID         string `json:"jid"`
	}
	if err := json.Unmarshal(res, &raw); err != nil {
		return nil, fmt.Errorf("parse devices: %w", err)
	}
	out := make([]Device, 0, len(raw))
	for _, d := range raw {
		out = append(out, Device{Name: d.DisplayName, Device: d.ID, JID: d.JID})
	}
	return out, nil
}

// Status is the only reliable "is this device usable" check — the device
// record's own state field can read "connected" before auth completes.
func (c *Client) Status(deviceID string) (*ConnStatus, error) {
	res, err := c.call(http.MethodGet, "/app/status", deviceQuery(deviceID), nil)
	if err != nil {
		return nil, err
	}
	var out ConnStatus
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("parse status: %w", err)
	}
	return &out, nil
}

func (c *Client) LoginQR(deviceID string) (*LoginQR, error) {
	res, err := c.call(http.MethodGet, "/app/login", deviceQuery(deviceID), nil)
	if err != nil {
		return nil, err
	}
	var out LoginQR
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("parse login response: %w", err)
	}
	return &out, nil
}

func (c *Client) LoginCode(deviceID, phone string) (*PairCode, error) {
	q := deviceQuery(deviceID)
	q.Set("phone", phone)
	res, err := c.call(http.MethodGet, "/app/login-with-code", q, nil)
	if err != nil {
		return nil, err
	}
	var out PairCode
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("parse pair code response: %w", err)
	}
	return &out, nil
}

func (c *Client) Reconnect(deviceID string) error {
	_, err := c.call(http.MethodGet, "/app/reconnect", deviceQuery(deviceID), nil)
	return err
}

// Logout unlinks the WhatsApp session for this device; it must be re-paired
// afterwards.
func (c *Client) Logout(deviceID string) error {
	_, err := c.call(http.MethodGet, "/app/logout", deviceQuery(deviceID), nil)
	return err
}

// AddDevice registers a fresh, unpaired device record and returns its id.
func (c *Client) AddDevice() (string, error) {
	res, err := c.call(http.MethodPost, "/devices", nil, strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	var out struct {
		ID       string `json:"id"`
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", fmt.Errorf("parse add-device response: %w", err)
	}
	if out.ID != "" {
		return out.ID, nil
	}
	if out.DeviceID != "" {
		return out.DeviceID, nil
	}
	return "", fmt.Errorf("add-device response had no id: %s", string(res))
}

// Fetch downloads a file gowa serves (e.g. a QR code PNG) so the admin UI can
// re-serve it without the browser needing gowa's credentials.
func (c *Client) Fetch(path string) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, "", err
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	return data, resp.Header.Get("Content-Type"), err
}
