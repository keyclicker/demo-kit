package main

import (
	"net/http/httptest"
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
