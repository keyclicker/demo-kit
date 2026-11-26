package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keyclicker/demo-kit/internal/config"
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
	tts := testEngine(srv.URL)

	ctx := context.Background()
	sp, err := tts.synthesize(ctx, Voice{Model: "m"}, "hi")
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

	usd, ok, err := tts.price(ctx, sp.GenID, 10*time.Second)
	if err != nil || !ok || usd != 0.0019 {
		t.Fatalf("price: %v %v %v", usd, ok, err)
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

// A busy API (in-flight budget full) is retried; other errors aren't
func TestSynthesizeRetriesBusy(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/audio/speech" {
				http.NotFound(w, r)
				return
			}
			if calls++; calls == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusPaymentRequired)
				w.Write([]byte(`{"error":{"metadata":` +
					`{"reason":"in_flight_budget_exhausted"}}}`))
				return
			}
			w.Write([]byte("pcm"))
		}))
	defer srv.Close()
	tts := testEngine(srv.URL)

	start := time.Now()
	sp, err := tts.synthesize(context.Background(), Voice{Model: "m"}, "hi")
	if err != nil || string(sp.Audio) != "pcm" || calls != 2 {
		t.Fatalf("got %+v, %v after %d calls", sp, err, calls)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("waited far too long")
	}
}

func TestRetryAfter(t *testing.T) {
	for in, want := range map[string]time.Duration{"120": 30 * time.Second,
		"2": 2 * time.Second, "": 5 * time.Second, "x": 5 * time.Second} {
		if got := retryAfter(in); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

// testEngine is an OpenAI-compatible engine at url, keyed "k".
func testEngine(url string) *openaiTTS {
	return &openaiTTS{base: url, keyEnv: "TEST_TTS_KEY", key: "k"}
}

// A keyless engine (a local server) is ready, sends no Authorization
// and, naming no generation, prices nothing. A keyed one sends its
// key, and without it isn't ready
func TestEngineKeys(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			auth = append(auth, r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "audio/wav")
			w.Write([]byte("wav"))
		}))
	defer srv.Close()

	local := newEngine(config.Config{TTS: config.TTS{URL: srv.URL + "/v1/"}})
	if err := local.ready(); err != nil {
		t.Fatalf("keyless not ready: %v", err)
	}
	sp, err := local.synthesize(context.Background(),
		Voice{Model: "kokoro"}, "hi")
	if err != nil || sp.GenID != "" || len(auth) != 1 || auth[0] != "" {
		t.Fatalf("keyless: %+v %v, auth %q", sp, err, auth)
	}
	if _, ok, err := local.(ttsPricer).price(context.Background(),
		sp.GenID, 0); ok ||
		err != nil || len(auth) != 1 {
		t.Fatalf("keyless price: ok %v, err %v, auth %q", ok, err, auth)
	}

	t.Setenv("TEST_TTS_KEY", " sk-1\n")
	keyed := newEngine(config.Config{TTS: config.TTS{URL: srv.URL,
		KeyEnv: "TEST_TTS_KEY"}})
	if _, err := keyed.synthesize(context.Background(),
		Voice{Model: "m"}, "hi"); err != nil || auth[1] != "Bearer sk-1" {
		t.Fatalf("keyed: %v, auth %q", err, auth)
	}

	t.Setenv("TEST_TTS_KEY", "")
	keyed = newEngine(config.Config{TTS: config.TTS{URL: srv.URL,
		KeyEnv: "TEST_TTS_KEY"}})
	if err := keyed.ready(); err == nil ||
		!strings.Contains(err.Error(), "TEST_TTS_KEY") {
		t.Fatalf("keyed without key: %v", err)
	}
	pricer := keyed.(ttsPricer)
	if _, ok, err := pricer.price(context.Background(), "gen-1", 0); ok ||
		err != nil {
		t.Fatalf("keyed price without key: %v %v", ok, err)
	}
}
