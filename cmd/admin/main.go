// Command admin serves the status/recovery web UI.
//
// By default it listens on 127.0.0.1 only (reach it via an SSH tunnel). To
// serve it on a LAN or ZeroTier address instead, set ADMIN_ADDR to those
// specific addresses and ADMIN_ALLOWED_NETS to the client networks that may
// connect — see .env.example.
//
// The page needs a login (username + password, stored as a hash). Create or
// reset it with `admin passwd`; on a fresh install the first account can also
// be created from the setup page, opened on the machine itself.
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tarkiman/claude-whatsapp/internal/admin"
	"github.com/tarkiman/claude-whatsapp/internal/adminauth"
	"github.com/tarkiman/claude-whatsapp/internal/config"
	"github.com/tarkiman/claude-whatsapp/internal/gowa"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "passwd" {
		os.Exit(runPasswd(passwdEnv{
			path: credentialsPath(), in: os.Stdin, out: os.Stdout, errw: os.Stderr,
			tty: stdinIsTerminal(), secret: ttySecret,
		}, os.Args[2:]))
	}

	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	addrs := splitCSV(os.Getenv("ADMIN_ADDR"))
	if len(addrs) == 0 {
		addrs = []string{"127.0.0.1:8098"}
	}
	nets, err := parseCIDRs(splitCSV(os.Getenv("ADMIN_ALLOWED_NETS")))
	if err != nil {
		log.Fatalf("ADMIN_ALLOWED_NETS: %v", err)
	}

	hosts := splitCSV(os.Getenv("ADMIN_ALLOWED_HOSTS"))
	for _, a := range addrs {
		host, _, err := net.SplitHostPort(a)
		if err != nil || host == "" {
			log.Fatalf("ADMIN_ADDR entry %q must be host:port", a)
		}
		ip := net.ParseIP(host)
		if ip != nil && ip.IsUnspecified() {
			log.Fatalf("ADMIN_ADDR entry %q listens on every interface — list the specific address(es) instead", a)
		}
		if !isLoopback(a) && len(nets) == 0 {
			log.Fatalf("ADMIN_ADDR entry %q is not loopback, so ADMIN_ALLOWED_NETS is required (e.g. 192.168.1.0/24)", a)
		}
		hosts = append(hosts, host)
	}

	logAuthState(cfg.AdminAuthFile, addrs)

	srv := admin.New(cfg, gowa.New(cfg.GowaBaseURL, cfg.GowaUser, cfg.GowaPass), admin.Options{
		AllowedNets:  nets,
		AllowedHosts: hosts,
	})

	for _, a := range addrs {
		go serve(a, srv.Handler())
	}
	select {}
}

// logAuthState adopts a password given the old way (ADMIN_PASSWORD in .env) on
// the first start after an upgrade, and says whether a login exists.
func logAuthState(path string, addrs []string) {
	creds := adminauth.Open(path, 0)
	legacy := os.Getenv("ADMIN_PASSWORD")
	adopted, err := adminauth.Bootstrap(creds, os.Getenv("ADMIN_USER"), legacy)
	if err != nil {
		log.Fatalf("admin: cannot adopt ADMIN_PASSWORD: %v", err)
	}
	exists, ferr := creds.State()
	switch {
	case adopted:
		log.Printf("admin: ADMIN_PASSWORD from .env was adopted as the login of user %q and is now stored only as a hash in %s — change it in the UI, then delete ADMIN_PASSWORD from .env", creds.Username(), path)
	case ferr != nil:
		log.Printf("admin: the login file is unusable (%v) — nobody can sign in until it is fixed: run `admin passwd` on this machine", ferr)
	case !exists:
		log.Printf("admin: no admin login exists yet — open http://%s on this machine to create it, or run `admin passwd` (other machines are refused until then)", addrs[0])
	default:
		log.Printf("admin: login enabled for user %q", creds.Username())
	}
	if legacy != "" && !adopted && exists {
		log.Printf("admin: ADMIN_PASSWORD in .env is ignored now that %s exists — delete it from .env", path)
	}
}

// serve retries binding forever: a ZeroTier address only exists once its
// interface is up, which can be after this service starts at boot. One
// address failing must not take the others down.
func serve(addr string, h http.Handler) {
	for {
		log.Printf("claude-whatsapp admin listening on http://%s", addr)
		err := http.ListenAndServe(addr, h)
		log.Printf("admin: %s: %v — retrying in 5s", addr, err)
		time.Sleep(5 * time.Second)
	}
}

func splitCSV(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func parseCIDRs(list []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, c := range list {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", c, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
