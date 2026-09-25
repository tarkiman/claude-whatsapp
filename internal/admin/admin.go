// Package admin is a small network-restricted web UI for checking the health of
// the bridge stack and for the recovery actions that otherwise need a
// terminal: re-pairing WhatsApp and inspecting which Claude account is in use.
//
// It runs as its own process (cmd/admin) rather than inside the bridge so it
// stays reachable exactly when the bridge is the thing that is broken.
package admin

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/config"
	"github.com/tarkiman/claude-whatsapp/internal/gowa"
)

//go:embed web
var webFS embed.FS

const (
	bridgeUnit    = "claude-whatsapp.service"
	claudeTTL     = 30 * time.Second
	qrPathPrefix  = "/statics/qrcode/"
	adminHeader   = "X-Admin-Request"
	cmdTimeout    = 10 * time.Second
	logLinesShown = 40
)

var watchedContainers = []string{"claude-whatsapp-gowa", "claude-whatsapp-gowa-media-perms-fix"}

// Options controls who may reach the UI beyond the loopback interface.
type Options struct {
	// AllowedNets are the client networks (besides loopback) that may connect.
	AllowedNets []*net.IPNet
	// AllowedHosts are the Host header values (no port) accepted in addition
	// to localhost/127.0.0.1/::1 — the address(es) the UI is served on.
	AllowedHosts []string
	// Password, if set, enables HTTP basic auth (user "admin") on everything.
	Password string
}

type Server struct {
	cfg  *config.Config
	gowa *gowa.Client
	mux  *http.ServeMux
	opts Options

	claudeMu  sync.Mutex
	claudeAt  time.Time
	claudeVal *ClaudeInfo

	login loginSession
}

func New(cfg *config.Config, g *gowa.Client, opts Options) *Server {
	s := &Server{cfg: cfg, gowa: g, mux: http.NewServeMux(), opts: opts}

	sub, _ := fs.Sub(webFS, "web")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))

	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/logs", s.handleLogs)
	s.mux.HandleFunc("POST /api/wa/qr", s.handleQR)
	s.mux.HandleFunc("GET /api/wa/qr.png", s.handleQRImage)
	s.mux.HandleFunc("POST /api/wa/pair-code", s.handlePairCode)
	s.mux.HandleFunc("POST /api/wa/reconnect", s.handleReconnect)
	s.mux.HandleFunc("POST /api/wa/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/wa/status", s.handleWAStatus)
	s.mux.HandleFunc("GET /api/claude/login", s.handleLoginState)
	s.mux.HandleFunc("POST /api/claude/login/start", s.handleLoginStart)
	s.mux.HandleFunc("POST /api/claude/login/code", s.handleLoginCode)
	s.mux.HandleFunc("POST /api/claude/login/cancel", s.handleLoginCancel)
	return s
}

func (s *Server) Handler() http.Handler { return s.guard(s.mux) }

func (s *Server) clientAllowed(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range s.opts.AllowedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) hostAllowed(hostHeader string) bool {
	host, _, err := net.SplitHostPort(hostHeader)
	if err != nil {
		host = strings.Trim(hostHeader, "[]")
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	for _, h := range s.opts.AllowedHosts {
		if h == host {
			return true
		}
	}
	return false
}

// guard defends the admin surface. Order matters: first drop clients outside
// the allowed networks, then check the optional password, then defend against
// the two ways a web page in an authorised operator's browser can still reach
// it — DNS rebinding (Host header) and cross-site POSTs (Origin plus a custom
// header that forces a CORS preflight).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.clientAllowed(r.RemoteAddr) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if s.opts.Password != "" {
			_, pass, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(s.opts.Password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="claude-whatsapp admin"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if !s.hostAllowed(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			if r.Header.Get(adminHeader) != "1" {
				http.Error(w, "missing "+adminHeader, http.StatusForbidden)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					http.Error(w, "cross-origin request refused", http.StatusForbidden)
					return
				}
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// `systemctl --user` / `journalctl --user` need the per-user runtime dir,
	// which is missing when launched from a plain shell rather than a user unit.
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid()))
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// --- status ---------------------------------------------------------------

type BridgeInfo struct {
	Unit      string `json:"unit"`
	Active    string `json:"active"`
	Since     string `json:"since,omitempty"`
	HealthOK  bool   `json:"healthOk"`
	Pending   int    `json:"pending"`
	Chats     int    `json:"chats"`
	HealthErr string `json:"healthErr,omitempty"`
}

type WAInfo struct {
	Reachable  bool            `json:"reachable"`
	Error      string          `json:"error,omitempty"`
	DeviceID   string          `json:"deviceId,omitempty"`
	JID        string          `json:"jid,omitempty"`
	Connected  bool            `json:"connected"`
	LoggedIn   bool            `json:"loggedIn"`
	NoDevice   bool            `json:"noDevice"`
	Containers []ContainerInfo `json:"containers"`
}

type ContainerInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Up     bool   `json:"up"`
}

type ClaudeInfo struct {
	LoggedIn     bool   `json:"loggedIn"`
	Email        string `json:"email,omitempty"`
	AuthMethod   string `json:"authMethod,omitempty"`
	Subscription string `json:"subscription,omitempty"`
	Error        string `json:"error,omitempty"`
}

type StatusResponse struct {
	Overall string     `json:"overall"`
	Reasons []string   `json:"reasons"`
	Bridge  BridgeInfo `json:"bridge"`
	WA      WAInfo     `json:"whatsapp"`
	Claude  ClaudeInfo `json:"claude"`
	Time    time.Time  `json:"time"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var resp StatusResponse
	resp.Time = time.Now()

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); resp.Bridge = s.bridgeInfo(ctx) }()
	go func() { defer wg.Done(); resp.WA = s.waInfo(ctx) }()
	go func() { defer wg.Done(); resp.Claude = *s.claudeInfo(ctx) }()
	wg.Wait()

	resp.Overall, resp.Reasons = verdict(resp)
	writeJSON(w, http.StatusOK, resp)
}

func verdict(r StatusResponse) (string, []string) {
	var down, warn []string
	if r.Bridge.Active != "active" {
		down = append(down, "bridge service is "+r.Bridge.Active)
	} else if !r.Bridge.HealthOK {
		down = append(down, "bridge is not answering /health")
	}
	switch {
	case !r.WA.Reachable:
		down = append(down, "gowa is unreachable")
	case r.WA.NoDevice:
		down = append(down, "no WhatsApp device is linked — pair one below")
	case !r.WA.LoggedIn:
		down = append(down, "WhatsApp is logged out — re-pair below")
	case !r.WA.Connected:
		warn = append(warn, "WhatsApp is disconnected — replies will not be delivered until it reconnects")
	}
	for _, c := range r.WA.Containers {
		if !c.Up {
			warn = append(warn, "container "+c.Name+" is not running")
		}
	}
	if !r.Claude.LoggedIn {
		down = append(down, "Claude CLI is not logged in — the bridge cannot answer")
	}
	if r.Bridge.Pending > 0 {
		warn = append(warn, fmt.Sprintf("%d message(s) waiting in the pending queue", r.Bridge.Pending))
	}

	switch {
	case len(down) > 0:
		return "down", append(down, warn...)
	case len(warn) > 0:
		return "degraded", warn
	}
	return "ok", nil
}

func (s *Server) bridgeInfo(ctx context.Context) BridgeInfo {
	b := BridgeInfo{Unit: bridgeUnit}

	out, _ := run(ctx, "systemctl", "--user", "is-active", bridgeUnit)
	b.Active = out
	if b.Active == "" {
		b.Active = "unknown"
	}
	if ts, err := run(ctx, "systemctl", "--user", "show", bridgeUnit, "-p", "ActiveEnterTimestamp", "--value"); err == nil {
		b.Since = ts
	}

	port := s.cfg.ListenAddr
	if strings.HasPrefix(port, ":") {
		port = "127.0.0.1" + port
	}
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(hctx, http.MethodGet, "http://"+port+"/health", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil {
		b.HealthErr = err.Error()
	} else {
		resp.Body.Close()
		b.HealthOK = resp.StatusCode == http.StatusOK
	}

	if entries, err := os.ReadDir(s.cfg.PendingDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				b.Pending++
			}
		}
	}
	if raw, err := os.ReadFile(s.cfg.SessionStorePath); err == nil {
		var m map[string]string
		if json.Unmarshal(raw, &m) == nil {
			b.Chats = len(m)
		}
	}
	return b
}

func (s *Server) waInfo(ctx context.Context) WAInfo {
	var w WAInfo

	if out, err := run(ctx, "docker", "ps", "-a", "--format", "{{.Names}}\t{{.Status}}"); err == nil {
		seen := map[string]ContainerInfo{}
		for _, line := range strings.Split(out, "\n") {
			name, status, ok := strings.Cut(line, "\t")
			if ok {
				seen[name] = ContainerInfo{Name: name, Status: status, Up: strings.HasPrefix(status, "Up")}
			}
		}
		for _, n := range watchedContainers {
			if c, ok := seen[n]; ok {
				w.Containers = append(w.Containers, c)
			} else {
				w.Containers = append(w.Containers, ContainerInfo{Name: n, Status: "not found"})
			}
		}
	}

	devs, err := s.gowa.Devices()
	if err != nil {
		w.Error = err.Error()
		return w
	}
	w.Reachable = true
	if len(devs) == 0 {
		w.NoDevice = true
		return w
	}
	w.DeviceID = devs[0].Device
	st, err := s.gowa.Status(w.DeviceID)
	if err != nil {
		w.Error = err.Error()
		return w
	}
	w.JID, w.Connected, w.LoggedIn = st.JID, st.IsConnected, st.IsLoggedIn
	return w
}

// claudeInfo reads `claude auth status`. Cached briefly because it shells out
// to the CLI and the dashboard polls every few seconds.
func (s *Server) claudeInfo(ctx context.Context) *ClaudeInfo {
	s.claudeMu.Lock()
	defer s.claudeMu.Unlock()
	if s.claudeVal != nil && time.Since(s.claudeAt) < claudeTTL {
		return s.claudeVal
	}

	info := &ClaudeInfo{}
	out, err := run(ctx, s.cfg.ClaudeBin, "auth", "status")
	var parsed struct {
		LoggedIn         bool   `json:"loggedIn"`
		Email            string `json:"email"`
		AuthMethod       string `json:"authMethod"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if jerr := json.Unmarshal([]byte(out), &parsed); jerr != nil {
		info.Error = "could not read `claude auth status`"
		if err != nil {
			info.Error += ": " + err.Error()
		}
	} else {
		info.LoggedIn = parsed.LoggedIn
		info.Email = parsed.Email
		info.AuthMethod = parsed.AuthMethod
		info.Subscription = parsed.SubscriptionType
	}
	s.claudeVal, s.claudeAt = info, time.Now()
	return info
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	which := r.URL.Query().Get("source")
	var (
		out string
		err error
	)
	switch which {
	case "gowa":
		out, err = run(r.Context(), "docker", "logs", "--tail", fmt.Sprint(logLinesShown), watchedContainers[0])
	default:
		out, err = run(r.Context(), "journalctl", "--user", "-u", bridgeUnit, "-t", "bridge", "-n", fmt.Sprint(logLinesShown), "--no-pager", "-o", "short-iso")
	}
	if err != nil && out == "" {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"source": which, "log": out})
}

// --- WhatsApp recovery ------------------------------------------------------

func (s *Server) handleWAStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("device_id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, errors.New("device_id required"))
		return
	}
	st, err := s.gowa.Status(id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// deviceForPairing returns the requested device if gowa still has it, else the
// first registered one, else registers a new one (after a logout, or on a
// fresh install). The UI may hold a stale id from before a logout removed it.
func (s *Server) deviceForPairing(requested string) (string, error) {
	id, err := s.existingDevice(requested)
	if err != nil || id != "" {
		return id, err
	}
	return s.gowa.AddDevice()
}

// existingDevice resolves a device that is already registered without ever
// creating one; it returns "" when gowa has none.
func (s *Server) existingDevice(requested string) (string, error) {
	devs, err := s.gowa.Devices()
	if err != nil {
		return "", err
	}
	for _, d := range devs {
		if d.Device == requested {
			return requested, nil
		}
	}
	if len(devs) > 0 {
		return devs[0].Device, nil
	}
	return "", nil
}

type deviceRequest struct {
	DeviceID string `json:"deviceId"`
	Phone    string `json:"phone"`
	Confirm  string `json:"confirm"`
}

func readReq(r *http.Request) deviceRequest {
	var req deviceRequest
	_ = json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(&req)
	return req
}

func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	req := readReq(r)
	id, err := s.deviceForPairing(req.DeviceID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	qr, err := s.gowa.LoginQR(id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	u, err := url.Parse(qr.QRLink)
	if err != nil || !strings.HasPrefix(u.Path, qrPathPrefix) {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("unexpected QR link from gowa: %q", qr.QRLink))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deviceId": id,
		"duration": qr.QRDuration,
		"image":    "/api/wa/qr.png?path=" + url.QueryEscape(u.Path),
	})
}

// handleQRImage proxies gowa's QR PNG. The path is restricted to gowa's QR
// directory so this can't be used to read arbitrary files off gowa with the
// admin's stored credentials.
func (s *Server) handleQRImage(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if !strings.HasPrefix(p, qrPathPrefix) || strings.Contains(p, "..") || !strings.HasSuffix(p, ".png") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	data, ctype, err := s.gowa.Fetch(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if ctype == "" {
		ctype = "image/png"
	}
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(data)
}

func (s *Server) handlePairCode(w http.ResponseWriter, r *http.Request) {
	req := readReq(r)
	phone := strings.TrimPrefix(strings.TrimSpace(req.Phone), "+")
	if phone == "" || strings.Trim(phone, "0123456789") != "" {
		writeErr(w, http.StatusBadRequest, errors.New("phone must be digits only, with country code (e.g. 62812...)"))
		return
	}
	id, err := s.deviceForPairing(req.DeviceID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	pc, err := s.gowa.LoginCode(id, phone)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deviceId": id, "code": pc.PairCode})
}

func (s *Server) handleReconnect(w http.ResponseWriter, r *http.Request) {
	req := readReq(r)
	id, err := s.existingDevice(req.DeviceID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if id == "" {
		writeErr(w, http.StatusConflict, errors.New("no WhatsApp device is registered — pair one first"))
		return
	}
	if err := s.gowa.Reconnect(id); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "reconnect requested"})
}

// handleLogout unlinks the WhatsApp session. Destructive (needs a re-scan), so
// the request must carry an explicit confirmation token in addition to the
// UI's own confirm dialog.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	req := readReq(r)
	if req.Confirm != "LOGOUT" {
		writeErr(w, http.StatusBadRequest, errors.New(`send {"confirm":"LOGOUT"} to unlink WhatsApp`))
		return
	}
	id, err := s.existingDevice(req.DeviceID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if id == "" {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "already unlinked — pair a new device to continue"})
		return
	}
	if err := s.gowa.Logout(id); err != nil {
		// When the WhatsApp session is already gone, gowa still purges the
		// device record but then errors out (observed: "the store doesn't
		// contain a device JID"). If the device is gone afterwards, the
		// goal was met.
		if left, lerr := s.existingDevice(id); lerr == nil && left != id {
			writeJSON(w, http.StatusOK, map[string]string{"ok": "unlinked — pair a new device to continue"})
			return
		}
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "unlinked — pair a new device to continue"})
}
