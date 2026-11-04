package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSynthesizeAndCost(t *testing.T) {
	var body map[string]any
	polls := 0

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// Local port probes hit test servers too: ignore them
			api := r.URL.Path == "/audio/speech" ||
				r.URL.Path == "/generation"
			if !api {
				http.NotFound(w, r)
				return
			}
			if auth := r.Header.Get("Authorization"); auth != "Bearer k" {
				t.Errorf("%s: auth header %q", r.URL, auth)
			}

			switch r.URL.Path {
			case "/audio/speech":
				json.NewDecoder(r.Body).Decode(&body)
				w.Header().Set("X-Generation-Id", "gen-1")
				w.Header().Set("Content-Type", "audio/pcm;rate=24000")
				w.Write([]byte("pcm"))

			case "/generation":
				// Unpriced on the first poll, like the real API
				if polls++; polls == 1 {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Write([]byte(`{"data":{"total_cost":0.0019}}`))
			}
		}))
	defer srv.Close()
	apiBase = srv.URL

	ctx := context.Background()
	sp, err := synthesize(ctx, "k", speechRequest{Model: "m", Input: "hi"})
	if err != nil || string(sp.Audio) != "pcm" || sp.GenID != "gen-1" {
		t.Fatalf("synthesize: %+v %v", sp, err)
	}

	// Empty voice and instructions are left to the provider
	if _, ok := body["voice"]; ok {
		t.Errorf("empty voice was sent: %v", body)
	}
	if body["response_format"] != "pcm" {
		t.Errorf("response_format: %v", body["response_format"])
	}

	usd, err := generationCost(ctx, "k", sp.GenID, 10*time.Second)
	if err != nil || usd != 0.0019 {
		t.Fatalf("cost: %v %v", usd, err)
	}
}

func TestPCMInput(t *testing.T) {
	cases := map[string]string{
		"audio/pcm;rate=24000;channels=1":   "-f s16le -ar 24000 -ac 1",
		"audio/pcm":                         "-f s16le -ar 24000 -ac 1",
		"audio/L16; rate=44100; channels=2": "-f s16le -ar 44100 -ac 2",
		"audio/mpeg":                        "",
		"":                                  "",
	}
	for ct, want := range cases {
		if got := strings.Join(pcmInput(ct), " "); got != want {
			t.Errorf("pcmInput(%q) = %q, want %q", ct, got, want)
		}
	}
}
