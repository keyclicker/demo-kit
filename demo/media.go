package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// renderNode is the 890M's render-only DRM node, for VAAPI encoding.
const renderNode = "/dev/dri/renderD128"

// ffmpeg runs ffmpeg quietly in dir (empty: here), overwriting
// outputs, with stdin as input.
func ffmpeg(ctx context.Context, dir string, stdin []byte,
	args ...string) error {

	base := []string{"-loglevel", "error", "-y"}
	if stdin == nil {
		// ffmpeg reads keys from a terminal stdin, and stalls on them
		base = append(base, "-nostdin")
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", append(base, args...)...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	if msg, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, strings.TrimSpace(string(msg)))
	}
	return nil
}

// probeDuration is a media file's length in seconds.
func probeDuration(ctx context.Context, path string) (float64, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-show_entries", "format=duration", "-of", "csv=p=0",
		path).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe %s: %w", path, err)
	}

	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// Chapter is one chapter of a finished video, in seconds.
type Chapter struct {
	Title      string
	Start, End float64
}

// probeChapters reads a video's chapters and length.
func probeChapters(ctx context.Context, path string) ([]Chapter,
	float64, error) {

	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-show_chapters", "-show_entries", "format=duration",
		"-of", "json", path).Output()
	if err != nil {
		return nil, 0, fmt.Errorf("ffprobe %s: %w", path, err)
	}

	var probe struct {
		Chapters []struct {
			Start string            `json:"start_time"`
			End   string            `json:"end_time"`
			Tags  map[string]string `json:"tags"`
		} `json:"chapters"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, 0, err
	}

	var chapters []Chapter
	for _, c := range probe.Chapters {
		start, _ := strconv.ParseFloat(c.Start, 64)
		end, _ := strconv.ParseFloat(c.End, 64)
		chapters = append(chapters, Chapter{c.Tags["title"], start, end})
	}

	length, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	return chapters, length, nil
}

// clock formats seconds as m:ss, for people.
func clock(secs float64) string {
	s := int(secs + 0.5)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// ---- Publishing paths ----

// publicDir is ~/public, which hub serves to the tailnet at /files/.
func publicDir() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, "public"), err
}

// publicURL is where the tailnet reaches a file in ~/public.
func publicURL(ctx context.Context, path string) (string, error) {
	public, err := publicDir()
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(public, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s: not in %s, so not served", path, public)
	}

	out, err := exec.CommandContext(ctx, "tailscale", "status",
		"--json").Output()
	if err != nil {
		return "", fmt.Errorf("tailscale status: %w", err)
	}

	var status struct{ Self struct{ DNSName string } }
	if json.Unmarshal(out, &status) != nil || status.Self.DNSName == "" {
		return "", fmt.Errorf("tailscale doesn't name this node")
	}

	u := url.URL{
		Scheme: "https",
		Host:   strings.TrimSuffix(status.Self.DNSName, "."),
		Path:   "/files/" + filepath.ToSlash(rel),
	}
	return u.String(), nil
}
