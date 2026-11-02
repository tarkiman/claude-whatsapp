package gowa

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeGowa is a minimal gowa stand-in for exercising the device_id
// resolve/cache/retry logic in post() without a real gowa instance.
type fakeGowa struct {
	mu          sync.Mutex
	devices     []Device        // served by GET /devices
	loggedIn    map[string]bool // which device ids /app/status reports as logged in
	devicesHits int
	gotHeaders  []string // X-Device-Id seen on each /send/message call
	rejectOnce  string   // if set, the next /send/message with this device id fails once
	srv         *httptest.Server
}

func newFakeGowa(t *testing.T) *fakeGowa {
	t.Helper()
	f := &fakeGowa{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/devices":
			f.mu.Lock()
			f.devicesHits++
			devs := f.devices
			f.mu.Unlock()
			if len(devs) == 0 {
				_, _ = w.Write([]byte(`{"code":"SUCCESS","results":null}`))
				return
			}
			raw, _ := json.Marshal(devicesPayload(devs))
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":` + string(raw) + `}`))
		case "/app/status":
			id := r.URL.Query().Get("device_id")
			f.mu.Lock()
			loggedIn := f.loggedIn[id]
			f.mu.Unlock()
			raw, _ := json.Marshal(map[string]any{"device_id": id, "is_logged_in": loggedIn, "is_connected": loggedIn})
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":` + string(raw) + `}`))
		case "/send/message", "/send/chat-presence":
			id := r.Header.Get("X-Device-Id")
			f.mu.Lock()
			f.gotHeaders = append(f.gotHeaders, id)
			reject := f.rejectOnce == id
			if reject {
				f.rejectOnce = ""
			}
			f.mu.Unlock()
			if id == "" || reject {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":"DEVICE_ID_REQUIRED","message":"device_id is required via X-Device-Id header or device_id query"}`))
				return
			}
			_, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"code":"SUCCESS","message":"ok"}`))
		default:
			_, _ = w.Write([]byte(`{"code":"SUCCESS","message":"ok"}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func devicesPayload(devs []Device) []map[string]string {
	out := make([]map[string]string, 0, len(devs))
	for _, d := range devs {
		out = append(out, map[string]string{"id": d.Device, "display_name": d.Name, "jid": d.JID})
	}
	return out
}

func TestSendResolvesAndCachesTheDeviceID(t *testing.T) {
	f := newFakeGowa(t)
	f.devices = []Device{{Device: "dev-1", Name: "phone", JID: "628@s.whatsapp.net"}}
	f.loggedIn = map[string]bool{"dev-1": true}
	c := New(f.srv.URL, "", "")

	if err := c.SendMessage("628@s.whatsapp.net", "hi"); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if err := c.SendMessage("628@s.whatsapp.net", "again"); err != nil {
		t.Fatalf("second send: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.devicesHits != 1 {
		t.Errorf("GET /devices called %d times, want 1 (the id should be cached after the first send)", f.devicesHits)
	}
	if len(f.gotHeaders) != 2 || f.gotHeaders[0] != "dev-1" || f.gotHeaders[1] != "dev-1" {
		t.Errorf("X-Device-Id headers = %v, want [dev-1 dev-1]", f.gotHeaders)
	}
}

func TestSendFailsClearlyWithNoDeviceLinked(t *testing.T) {
	f := newFakeGowa(t) // no devices registered
	c := New(f.srv.URL, "", "")

	err := c.SendMessage("628@s.whatsapp.net", "hi")
	if err == nil {
		t.Fatal("expected an error when no WhatsApp device is linked")
	}
}

func TestSendRefreshesAStaleDeviceIDOnce(t *testing.T) {
	f := newFakeGowa(t)
	f.devices = []Device{{Device: "dev-old", Name: "phone", JID: "628@s.whatsapp.net"}}
	f.loggedIn = map[string]bool{"dev-old": true}
	c := New(f.srv.URL, "", "")

	// Prime the cache with the current (soon to be stale) device id.
	if err := c.SendMessage("628@s.whatsapp.net", "prime"); err != nil {
		t.Fatalf("priming send: %v", err)
	}

	// Simulate a re-pair: gowa now has a different device id, and the next
	// send with the OLD cached id is rejected exactly once.
	f.mu.Lock()
	f.devices = []Device{{Device: "dev-new", Name: "phone", JID: "628@s.whatsapp.net"}}
	f.loggedIn = map[string]bool{"dev-new": true}
	f.rejectOnce = "dev-old"
	f.mu.Unlock()

	if err := c.SendMessage("628@s.whatsapp.net", "after re-pair"); err != nil {
		t.Fatalf("send after re-pair: %v (should have refreshed the device id and retried)", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	last := f.gotHeaders[len(f.gotHeaders)-1]
	if last != "dev-new" {
		t.Errorf("last X-Device-Id = %q, want dev-new (the refreshed id)", last)
	}
	if f.devicesHits != 2 {
		t.Errorf("GET /devices called %d times, want 2 (once to prime, once to refresh after the rejection)", f.devicesHits)
	}
}

// TestSendPicksTheLoggedInDeviceAmongSeveral reproduces a real registry
// state seen on a live Pi: three device records (two stale leftovers from
// earlier, never-completed pairing attempts, one actually logged in), where
// the logged-in one is NOT first in /devices' list. Blindly taking the first
// entry would silently try to send from a disconnected device.
func TestSendPicksTheLoggedInDeviceAmongSeveral(t *testing.T) {
	f := newFakeGowa(t)
	f.devices = []Device{
		{Device: "stale-1", Name: "", JID: ""},
		{Device: "stale-2", Name: "", JID: ""},
		{Device: "real", Name: "Tarkiman's Assistant", JID: "628@s.whatsapp.net"},
	}
	f.loggedIn = map[string]bool{"real": true}
	c := New(f.srv.URL, "", "")

	if err := c.SendMessage("628@s.whatsapp.net", "hi"); err != nil {
		t.Fatalf("send: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.gotHeaders) != 1 || f.gotHeaders[0] != "real" {
		t.Errorf("X-Device-Id = %v, want [real] (the logged-in device, not the first in the list)", f.gotHeaders)
	}
}

func TestChatPresenceAlsoCarriesTheDeviceID(t *testing.T) {
	f := newFakeGowa(t)
	f.devices = []Device{{Device: "dev-1", Name: "phone", JID: "628@s.whatsapp.net"}}
	f.loggedIn = map[string]bool{"dev-1": true}
	c := New(f.srv.URL, "", "")

	// SetChatPresence posts to /send/chat-presence, a separate path from
	// /send/message — it must carry a resolved device id too, not just the
	// message-sending call.
	if err := c.SetChatPresence("628@s.whatsapp.net", "start"); err != nil {
		t.Fatalf("presence: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.gotHeaders) != 1 || f.gotHeaders[0] != "dev-1" {
		t.Errorf("X-Device-Id on /send/chat-presence = %v, want [dev-1]", f.gotHeaders)
	}
}
