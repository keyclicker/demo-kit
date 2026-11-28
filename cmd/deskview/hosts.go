package main

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// DNS rebinding: a site that points its own name at 127.0.0.1 is the
// same origin as deskview by construction (its Origin's host is its
// Host), so origin checks can't stop it reading the page's API, the
// files or a display. The Host header can: a browser sets it from the
// URL, and the site's name isn't one deskview answers to.
//
// What `tailscale serve` passes on: the incoming Host as is, plus that
// same name as X-Forwarded-Host. So proxied, Host is the public name,
// which deskview.hosts lists. A proxy that rewrites Host to its
// upstream's (nginx's default) hides the name there and carries it in
// X-Forwarded-Host alone, so those names must be known as well. That
// header only ever refuses: it never makes a host known, as a rebinding
// page's fetch can set it.

// knownHosts answers only requests naming deskview (knownHost) in Host
// and in every X-Forwarded-Host; anything else is a 403 naming the
// setting.
func knownHosts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, host := range requestHosts(r) {
			if knownHost(host) {
				continue
			}

			http.Error(w, fmt.Sprintf("deskview: unknown host %q: add it "+
				"to deskview.hosts if it's a proxy's", host),
				http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// requestHosts lists the names a request says it is for: its Host, then
// those in its X-Forwarded-Host headers (each proxy in a chain may add
// one, comma-separated).
func requestHosts(r *http.Request) []string {
	hosts := []string{r.Host}

	for _, value := range r.Header.Values("X-Forwarded-Host") {
		for _, host := range strings.Split(value, ",") {
			if host = strings.TrimSpace(host); host != "" {
				hosts = append(hosts, host)
			}
		}
	}

	return hosts
}

// knownHost reports whether host (host or host:port) is localhost, an
// IP address or in deskview.hosts, any port, case and a trailing dot
// aside. Any address: rebinding needs a name to repoint, and an address
// deskview.addr listens on is deskview's own.
func knownHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))

	if host == "localhost" {
		return true
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}

	return slices.ContainsFunc(cfg.Deskview.Hosts, func(h string) bool {
		return strings.EqualFold(strings.TrimSuffix(h, "."), host)
	})
}
