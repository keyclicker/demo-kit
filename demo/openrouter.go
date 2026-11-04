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
	"time"
)

var apiBase = "https://openrouter.ai/api/v1"

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
// (Gemini TTS takes nothing else).
func synthesize(ctx context.Context, key string,
	req speechRequest) (speech, error) {

	req.ResponseFormat = "pcm"
	body, err := json.Marshal(req)
	if err != nil {
		return speech{}, err
	}

	res, err := call(ctx, key, http.MethodPost, "/audio/speech", body)
	if err != nil {
		return speech{}, err
	}
	defer res.Body.Close()

	audio, err := io.ReadAll(res.Body)
	if err != nil {
		return speech{}, err
	}
	if res.StatusCode != http.StatusOK {
		return speech{}, fmt.Errorf("%s %s: %s", req.Model, res.Status, audio)
	}

	return speech{
		Audio:       audio,
		ContentType: res.Header.Get("Content-Type"),
		GenID:       res.Header.Get("X-Generation-Id"),
	}, nil
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

	return http.DefaultClient.Do(req)
}
