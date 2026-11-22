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
	"time"
)

var apiBase = "https://openrouter.ai/api/v1"

// apiClient gives up on a stalled request: a demo build must end
var apiClient = &http.Client{Timeout: 2 * time.Minute}

// speechRequest is the body of POST /audio/speech. Empty fields are
// left out, so the provider's defaults apply.
type speechRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice,omitempty"`
	Instructions   string `json:"instructions,omitempty"`
	ResponseFormat string `json:"response_format"`
}

// speech is synthesized audio as the API returned it.
type speech struct {
	Audio       []byte
	ContentType string // e.g. "audio/pcm;rate=24000;channels=1"
	GenID       string // for generationCost
}

// synthesize requests raw PCM: the one format every provider accepts
// (Gemini TTS takes nothing else). It waits out rate limits and a
// full in-flight budget (OpenRouter reserves each running request's
// worst case against the key's limit) a few times before failing.
func synthesize(ctx context.Context, key string,
	req speechRequest) (speech, error) {

	req.ResponseFormat = "pcm"
	body, err := json.Marshal(req)
	if err != nil {
		return speech{}, err
	}

	for attempt := 1; ; attempt++ {
		sp, wait, err := synthesizeOnce(ctx, key, req.Model, body)
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
func synthesizeOnce(ctx context.Context, key, model string,
	body []byte) (speech, time.Duration, error) {

	res, err := call(ctx, key, http.MethodPost, "/audio/speech", body)
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

// generationCost polls GET /generation until OpenRouter has priced the
// request (it lags a few seconds), giving up after `wait`.
func generationCost(ctx context.Context, key, id string,
	wait time.Duration) (float64, error) {

	if id == "" {
		return 0, fmt.Errorf("no generation ID to price")
	}
	deadline := time.Now().Add(wait)

	for {
		res, err := call(ctx, key, http.MethodGet,
			"/generation?id="+url.QueryEscape(id), nil)
		if err != nil {
			return 0, err
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
			return gen.Data.TotalCost, nil
		case ok:
			return 0, fmt.Errorf("generation %s: %w", id, err)
		case time.Now().After(deadline):
			return 0, fmt.Errorf("generation %s: %s", id, res.Status)
		}
		time.Sleep(2 * time.Second)
	}
}

// call sends an authenticated request to the OpenRouter API.
func call(ctx context.Context, key, method, path string,
	body []byte) (*http.Response, error) {

	req, err := http.NewRequestWithContext(ctx, method, apiBase+path,
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	return apiClient.Do(req)
}
