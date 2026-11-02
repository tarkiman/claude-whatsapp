package admin

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Login for the Admin UI. Everything except the login page and its own
// endpoints needs a session cookie; the account (one username + a password
// stored only as a hash) is created at install time, from the loopback setup
// page, or with `bin/admin passwd`.

const (
	cookieName  = "cw_admin"
	sessionIdle = 30 * time.Minute
	sessionMax  = 12 * time.Hour
)

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func loopbackRemote(r *http.Request) bool {
	ip := net.ParseIP(clientIP(r))
	return ip != nil && ip.IsLoopback()
}

func isPublicPath(p string) bool {
	switch p {
	case "/login", "/api/login", "/api/setup", "/api/auth/state":
		return true
	}
	return false
}

func (s *Server) sessionOK(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return s.sessions.Valid(c.Value, s.creds.Fingerprint())
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	tok := s.sessions.Create(s.creds.Fingerprint())
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    tok,
		Path:     "/",
		MaxAge:   int(sessionMax / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// Over plain HTTP on a LAN the flag can't be set; behind TLS it is.
		Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
	})
}

func (s *Server) tooManyAttempts(w http.ResponseWriter, wait time.Duration) {
	secs := int(wait.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	mins := (secs + 59) / 60
	writeErr(w, http.StatusTooManyRequests, fmt.Errorf("too many attempts — try again in about %d minute(s)", mins))
}

func (s *Server) handleLoginPage(w http.ResponseWriter, _ *http.Request) {
	page, err := webFS.ReadFile("web/login.html")
	if err != nil {
		http.Error(w, "login page missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}

type authStateView struct {
	State         string `json:"state"` // "login", "setup" or "broken"
	SetupAllowed  bool   `json:"setupAllowed"`
	Authenticated bool   `json:"authenticated"`
	User          string `json:"user,omitempty"`
	TOTPEnabled   bool   `json:"totpEnabled,omitempty"`
	Error         string `json:"error,omitempty"`
}

func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	exists, err := s.creds.State()
	v := authStateView{State: "login"}
	switch {
	case err != nil:
		v.State, v.Error = "broken", err.Error()
	case !exists:
		v.State = "setup"
		v.SetupAllowed = loopbackRemote(r)
	}
	if v.State == "login" && s.sessionOK(r) {
		v.Authenticated, v.User = true, s.creds.Username()
		v.TOTPEnabled = s.creds.TOTPEnabled()
	}
	writeJSON(w, http.StatusOK, v)
}

type credsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTPCode string `json:"totpCode,omitempty"`
}

// handleSetup creates the very first account. It is only open while no
// account exists AND the request comes from the machine itself, so a page
// reachable over the LAN can never be claimed by someone else.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	exists, err := s.creds.State()
	switch {
	case err != nil:
		writeErr(w, http.StatusConflict, errors.New("the credentials file is unreadable — fix or delete it, or run `bin/admin passwd` on this machine"))
		return
	case exists:
		writeErr(w, http.StatusConflict, errors.New("an admin account already exists"))
		return
	case !loopbackRemote(r):
		writeErr(w, http.StatusForbidden, errors.New("first-time setup is only allowed from the machine itself — open this page there, or run `bin/admin passwd`"))
		return
	}
	var req credsRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if err := s.creds.Set(req.Username, req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.startSession(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"user": req.Username})
}

// handleLogin is a one- or two-step login depending on whether 2FA is on.
// Username+password are verified first and on their own; if that succeeds
// and 2FA is enabled but no code was sent yet, it responds with
// totpRequired instead of a session, and the SAME form resubmits
// username+password+code together — no separate "partially authenticated"
// server-side state is needed, since the password is simply checked again.
// A wrong code at that point is rate-limited exactly like a wrong password:
// by then the caller has already proven they know it, so repeated guessing
// is the same kind of attack.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ok, wait := s.limiter.Check(ip); !ok {
		s.tooManyAttempts(w, wait)
		return
	}
	var req credsRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if !s.creds.Verify(req.Username, req.Password) {
		s.limiter.Fail(ip)
		time.Sleep(s.failDelay)
		writeErr(w, http.StatusUnauthorized, errors.New("wrong username or password"))
		return
	}
	if s.creds.TOTPEnabled() {
		if req.TOTPCode == "" {
			writeJSON(w, http.StatusOK, map[string]bool{"totpRequired": true})
			return
		}
		if !s.creds.VerifyTOTPCode(req.TOTPCode) {
			s.limiter.Fail(ip)
			time.Sleep(s.failDelay)
			writeErr(w, http.StatusUnauthorized, errors.New("wrong authentication code"))
			return
		}
	}
	s.limiter.Success(ip)
	s.startSession(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"user": req.Username})
}

func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.sessions.Revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]string{"ok": "signed out"})
}

// handleChangePassword needs the current password again (a stolen session
// alone cannot lock the owner out), signs every other browser out and keeps
// this one signed in.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ok, wait := s.limiter.Check(ip); !ok {
		s.tooManyAttempts(w, wait)
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	user := s.creds.Username()
	if !s.creds.Verify(user, req.Current) {
		s.limiter.Fail(ip)
		time.Sleep(s.failDelay)
		writeErr(w, http.StatusUnauthorized, errors.New("the current password is wrong"))
		return
	}
	s.limiter.Success(ip)
	if req.New == req.Current {
		writeErr(w, http.StatusBadRequest, errors.New("the new password must be different from the current one"))
		return
	}
	if err := s.creds.Set(user, req.New); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.sessions.RevokeAll()
	s.startSession(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "password changed — other browsers were signed out"})
}

// handleTOTPSetup starts (or restarts) 2FA enrollment: a fresh secret is
// generated and stored as pending — not yet required at login — until
// handleTOTPConfirm proves it was captured correctly.
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	secret, uri, err := s.creds.BeginTOTPEnrollment()
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "uri": uri})
}

// handleTOTPConfirm turns 2FA on once the operator's authenticator app
// proves it has the secret by showing a code that matches.
func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ok, wait := s.limiter.Check(ip); !ok {
		s.tooManyAttempts(w, wait)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if err := s.creds.ConfirmTOTP(req.Code); err != nil {
		s.limiter.Fail(ip)
		time.Sleep(s.failDelay)
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.limiter.Success(ip)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "2FA enabled"})
}

// handleTOTPDisable needs the current password again — same reasoning as
// handleChangePassword: a stolen session alone must not be able to weaken
// the account by turning 2FA off.
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ok, wait := s.limiter.Check(ip); !ok {
		s.tooManyAttempts(w, wait)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if !s.creds.Verify(s.creds.Username(), req.Password) {
		s.limiter.Fail(ip)
		time.Sleep(s.failDelay)
		writeErr(w, http.StatusUnauthorized, errors.New("the current password is wrong"))
		return
	}
	s.limiter.Success(ip)
	if err := s.creds.DisableTOTP(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "2FA disabled"})
}
