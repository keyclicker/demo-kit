package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSameOrigin pins which Origin headers may open a VNC socket.
func TestSameOrigin(t *testing.T) {
	tests := []struct {
		name, host, origin, fwd string
		want                    bool
	}{
		{"no origin", "127.0.0.1:8737", "", "", true},
		{"same host", "127.0.0.1:8737", "http://127.0.0.1:8737", "", true},
		{"forwarded host", "127.0.0.1:8737", "https://box.example",
			"box.example", true},
		{"foreign host", "127.0.0.1:8737", "https://evil.example",
			"box.example", false},
		{"foreign, no forwarded", "127.0.0.1:8737", "https://evil.example",
			"", false},
		{"null", "127.0.0.1:8737", "null", "", false},
		{"null, forwarded", "127.0.0.1:8737", "null", "box.example", false},
		{"file", "127.0.0.1:8737", "file://", "", false},
		{"garbage", "127.0.0.1:8737", "::", "", false},
		{"other port", "127.0.0.1:8737", "http://127.0.0.1:8738", "", false},
		{"no port", "127.0.0.1:8737", "http://127.0.0.1", "", false},
		{"case differs", "box.example", "https://BOX.example", "", false},
	}

	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/vnc/0", nil)
		r.Host = tt.host
		if tt.origin != "" {
			r.Header.Set("Origin", tt.origin)
		}
		if tt.fwd != "" {
			r.Header.Set("X-Forwarded-Host", tt.fwd)
		}

		if got := sameOrigin(r); got != tt.want {
			t.Errorf("%s: sameOrigin(Origin %q, Host %q, fwd %q) = %v",
				tt.name, tt.origin, tt.host, tt.fwd, got)
		}
	}
}

// Only localhost, addresses and deskview.hosts are answered, whatever
// a request's headers claim
func TestKnownHosts(t *testing.T) {
	defer func(hosts []string) { cfg.Deskview.Hosts = hosts }(
		cfg.Deskview.Hosts)
	cfg.Deskview.Hosts = []string{"box.tailnet.ts.net"}

	ok := knownHosts(http.HandlerFunc(func(w http.ResponseWriter,
		_ *http.Request) {
	}))

	const box, loopback = "box.tailnet.ts.net", "127.0.0.1:8737"

	for _, tt := range []struct {
		host, fwd string
		want      int
	}{
		// A known forwarded host never makes Host known
		{"127.0.0.1:8737", box, 200},
		{"localhost:8737", box, 200},
		{"[::1]:8737", box, 200},
		{"localhost", box, 200},
		{"box.tailnet.ts.net", box, 200},
		{"BOX.tailnet.ts.net.:443", box, 200},
		{"192.0.2.7:8737", box, 200},
		{"[2001:db8::7]:8737", box, 200},
		{"evil.example:8737", box, 403},
		{"", box, 403},
		{"box", box, 403},
		{"box.tailnet.ts.net.evil.example", box, 403},
		{"127.0.0.1.evil.example", box, 403},

		// A proxy that rewrites Host: the forwarded names count
		{loopback, "", 200},
		{loopback, "evil.example", 403},
		{loopback, box + ", evil.example", 403},
		{loopback, "evil.example, " + box, 403},
		{loopback, box + ", localhost:8737", 200},
	} {
		r := httptest.NewRequest("GET", "/api/desktops", nil)
		r.Host = tt.host
		if tt.fwd != "" {
			r.Header.Set("X-Forwarded-Host", tt.fwd)
		}

		w := httptest.NewRecorder()
		ok.ServeHTTP(w, r)
		if w.Code != tt.want {
			t.Errorf("Host %q, fwd %q: %d, want %d", tt.host, tt.fwd,
				w.Code, tt.want)
		}
		if tt.want == 403 && !strings.Contains(w.Body.String(),
			"deskview.hosts") {
			t.Errorf("Host %q, fwd %q: %q names no setting", tt.host,
				tt.fwd, w.Body.String())
		}
	}
}
