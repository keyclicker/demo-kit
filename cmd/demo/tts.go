package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/keyclicker/demo-kit/internal/config"
)

// ===== Engines =====

// ttsEngine turns text into speech. openaiTTS, any OpenAI-compatible
// server (hosted or local), is the one there is; a local engine run as
// a program (piper, a Python script) would be another.
type ttsEngine interface {
	// ready says why the engine can't synthesize (no key, no binary),
	// before a build spends time on a line it can't buy.
	ready() error

	// synthesize speaks req.Input in req's voice.
	synthesize(ctx context.Context, req speechRequest) (speech, error)

	// price looks up what a synthesis cost by its speech.GenID; ok is
	// false when the engine can't tell (no pricing API, no key).
	price(ctx context.Context, genID string,
		wait time.Duration) (usd float64, ok bool, err error)
}

// speechRequest is the body of POST /audio/speech. Empty fields are
// left out, so the provider's defaults apply.
type speechRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice,omitempty"`
	Instructions   string `json:"instructions,omitempty"`
	ResponseFormat string `json:"response_format"`
}

// speech is synthesized audio as the engine returned it.
type speech struct {
	Audio       []byte
	ContentType string // e.g. "audio/pcm;rate=24000;channels=1"
	GenID       string // for price; "" when the engine has none
}

// newEngine returns the engine config tts describes. Its key is read
// now but needed only to synthesize: a cached build works without one.
func newEngine(t config.TTS) ttsEngine {
	e := &openaiTTS{base: strings.TrimSuffix(t.URL, "/"), keyEnv: t.KeyEnv}
	if t.KeyEnv != "" {
		e.key = strings.TrimSpace(os.Getenv(t.KeyEnv))
	}
	return e
}

// ===== OpenAI-compatible API =====

// openaiTTS speaks through POST {base}/audio/speech: OpenRouter, OpenAI,
// or a local server (Kokoro-FastAPI, Speaches, LocalAI). Prices come
// from OpenRouter's GET /generation, when a response names one.
type openaiTTS struct {
	base   string // e.g. "https://openrouter.ai/api/v1"
	keyEnv string // where key came from; "" means none is sent
	key    string
}

// ttsClient gives up on a stalled request: a demo build must end
var ttsClient = &http.Client{Timeout: 2 * time.Minute}

// ready fails when the key is due but unset.
func (e *openaiTTS) ready() error {
	if e.keyEnv == "" || e.key != "" {
		return nil
	}

	path, _ := config.EnvPath()
	return fmt.Errorf("no TTS key: set $%[1]s, or put %[1]s=... in %[2]s "+
		"(chmod 600); a server without keys: tts.key_env = \"\"",
		e.keyEnv, path)
}

// synthesize requests raw PCM: the one format every provider accepts
// (Gemini TTS takes nothing else). It waits out rate limits and a
// full in-flight budget (OpenRouter reserves each running request's
// worst case against the key's limit) a few times before failing.
func (e *openaiTTS) synthesize(ctx context.Context,
	req speechRequest) (speech, error) {

	if err := e.ready(); err != nil {
		return speech{}, err
	}

	req.ResponseFormat = "pcm"
	body, err := json.Marshal(req)
	if err != nil {
		return speech{}, err
	}

	for attempt := 1; ; attempt++ {
		sp, wait, err := e.synthesizeOnce(ctx, req.Model, body)
		if wait == 0 || attempt == maxAttempts {
			return sp, err
		}

		fmt.Fprintf(os.Stderr, "demo: speech API busy, retry %d/%d in %s\n",
			attempt, maxAttempts-1, wait)
		select {
		case <-ctx.Done():
			return speech{}, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// maxAttempts bounds synthesize's retries of a busy API.
const maxAttempts = 4

// synthesizeOnce sends one request. A busy API (429, or 402 from a
// full in-flight budget) returns a wait before retrying, else 0.
func (e *openaiTTS) synthesizeOnce(ctx context.Context, model string,
	body []byte) (speech, time.Duration, error) {

	res, err := e.call(ctx, http.MethodPost, "/audio/speech", body)
	if err != nil {
		return speech{}, 0, err
	}
	defer res.Body.Close()

	audio, err := io.ReadAll(res.Body)
	if err != nil {
		return speech{}, 0, err
	}

	if res.StatusCode != http.StatusOK {
		err := fmt.Errorf("%s %s: %s", model, res.Status, audio)
		busy := res.StatusCode == http.StatusTooManyRequests ||
			(res.StatusCode == http.StatusPaymentRequired &&
				bytes.Contains(audio, []byte("in_flight")))
		if busy {
			return speech{}, retryAfter(res.Header.Get("Retry-After")), err
		}
		return speech{}, 0, err
	}
	if len(audio) == 0 {
		return speech{}, 0, fmt.Errorf("%s: empty audio", model)
	}

	return speech{
		Audio:       audio,
		ContentType: res.Header.Get("Content-Type"),
		GenID:       res.Header.Get("X-Generation-Id"),
	}, 0, nil
}

// retryAfter reads a Retry-After in seconds, capped at 30 s: in-flight
// requests settle in seconds, whatever the header says.
func retryAfter(header string) time.Duration {
	secs, err := strconv.Atoi(header)
	if err != nil || secs <= 0 {
		secs = 5
	}
	return time.Duration(min(secs, 30)) * time.Second
}

// price polls GET /generation until OpenRouter has priced the request
// (it lags a few seconds), giving up after wait. Without a key it
// can't ask: unknown, not an error.
func (e *openaiTTS) price(ctx context.Context, id string,
	wait time.Duration) (float64, bool, error) {

	if id == "" || (e.keyEnv != "" && e.key == "") {
		return 0, false, nil
	}
	deadline := time.Now().Add(wait)

	for {
		res, err := e.call(ctx, http.MethodGet,
			"/generation?id="+url.QueryEscape(id), nil)
		if err != nil {
			return 0, false, err
		}

		var gen struct {
			Data struct {
				TotalCost float64 `json:"total_cost"`
			} `json:"data"`
		}
		ok := res.StatusCode == http.StatusOK
		if ok {
			err = json.NewDecoder(res.Body).Decode(&gen)
		}
		res.Body.Close()

		switch {
		case ok && err == nil:
			return gen.Data.TotalCost, true, nil
		case ok:
			return 0, false, fmt.Errorf("generation %s: %w", id, err)
		case time.Now().After(deadline):
			return 0, false, fmt.Errorf("generation %s: %s", id, res.Status)
		}
		time.Sleep(2 * time.Second)
	}
}

// call sends a request to the API, with the key when there is one.
func (e *openaiTTS) call(ctx context.Context, method, path string,
	body []byte) (*http.Response, error) {

	req, err := http.NewRequestWithContext(ctx, method, e.base+path,
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	req.Header.Set("Content-Type", "application/json")

	return ttsClient.Do(req)
}

// ===== Decoding =====

// pcmInput returns ffmpeg input flags for raw PCM content types
// ("audio/pcm;rate=24000;channels=1"), s16le assumed; nil for
// containers ffmpeg can probe itself.
func pcmInput(contentType string) []string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "audio/pcm" && mediaType != "audio/l16") {
		return nil
	}

	rate, channels := params["rate"], params["channels"]
	if rate == "" {
		rate = "24000"
	}
	if channels == "" {
		channels = "1"
	}
	return []string{"-f", "s16le", "-ar", rate, "-ac", channels}
}
