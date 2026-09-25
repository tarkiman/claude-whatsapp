// Command admin serves the status/recovery web UI.
//
// By default it listens on 127.0.0.1 only (reach it via an SSH tunnel). To
// serve it on a LAN or ZeroTier address instead, set ADMIN_ADDR to those
// specific addresses and ADMIN_ALLOWED_NETS to the client networks that may
// connect — see .env.example.
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
	"github.com/tarkiman/claude-whatsapp/internal/config"
	"github.com/tarkiman/claude-whatsapp/internal/gowa"
)

func main() {
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

	password := os.Getenv("ADMIN_PASSWORD")
	if password == "" {
		log.Printf("admin: ADMIN_PASSWORD is not set — access is limited by network only")
	}

	srv := admin.New(cfg, gowa.New(cfg.GowaBaseURL, cfg.GowaUser, cfg.GowaPass), admin.Options{
		AllowedNets:  nets,
		AllowedHosts: hosts,
		Password:     password,
	})

	for _, a := range addrs {
		go serve(a, srv.Handler())
	}
	select {}
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
