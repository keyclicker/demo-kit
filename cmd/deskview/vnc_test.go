package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSameOrigin pins which Origin headers may open a VNC socket.
func TestSameOrigin(t *testing.T) {
	defer func(hosts []string) { cfg.Deskview.Hosts = hosts }(
		cfg.Deskview.Hosts)
	cfg.Deskview.Hosts = []string{"box.example"}

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
		{"forwarded, unknown", "127.0.0.1:8737", "https://evil.example",
			"evil.example", false},
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

// Only loopback names and deskview.hosts are answered, whatever a
// request's headers claim
func TestKnownHosts(t *testing.T) {
	defer func(hosts []string) { cfg.Deskview.Hosts = hosts }(
		cfg.Deskview.Hosts)
	cfg.Deskview.Hosts = []string{"box.tailnet.ts.net"}

	ok := knownHosts(http.HandlerFunc(func(w http.ResponseWriter,
		_ *http.Request) {
	}))

	for host, want := range map[string]int{
		"127.0.0.1:8737": 200, "localhost:8737": 200, "[::1]:8737": 200,
		"localhost": 200, "box.tailnet.ts.net": 200,
		"BOX.tailnet.ts.net.:443": 200,
		"evil.example:8737":       403, "127.0.0.2:8737": 403, "": 403,
		"box.tailnet.ts.net.evil.example": 403,
	} {
		r := httptest.NewRequest("GET", "/api/desktops", nil)
		r.Host = host
		r.Header.Set("X-Forwarded-Host", "box.tailnet.ts.net")

		w := httptest.NewRecorder()
		ok.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q: %d, want %d", host, w.Code, want)
		}
		if want == 403 && !strings.Contains(w.Body.String(),
			"deskview.hosts") {
			t.Errorf("Host %q: %q names no setting", host, w.Body.String())
		}
	}
}
