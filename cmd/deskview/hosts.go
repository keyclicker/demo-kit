package main

import (
	"fmt"
	"net"
	"net/http"
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
// which deskview.hosts lists. A request header (X-Forwarded-Host) is
// never what makes a host known: a rebinding page's fetch can set it.

// knownHosts answers only requests whose Host names deskview
// (knownHost); anything else is a 403 naming the setting.
func knownHosts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !knownHost(r.Host) {
			http.Error(w, fmt.Sprintf("deskview: unknown host %q: add it "+
				"to deskview.hosts if it's a proxy's", r.Host),
				http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// knownHost reports whether host (host or host:port) is loopback's
// (localhost, 127.0.0.1, ::1) or in deskview.hosts, any port, case and
// a trailing dot aside.
func knownHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))

	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	case "":
		return false
	}

	return slices.ContainsFunc(cfg.Deskview.Hosts, func(h string) bool {
		return strings.EqualFold(strings.TrimSuffix(h, "."), host)
	})
}
