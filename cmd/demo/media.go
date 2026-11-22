package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	// renderNode is the 890M's render-only DRM node, for VAAPI encoding
	renderNode = "/dev/dri/renderD128"

	ffmpegTimeout = 10 * time.Minute
)

// ffmpeg runs ffmpeg quietly in dir (empty: here), overwriting
// outputs, with stdin (if any) as input.
func ffmpeg(ctx context.Context, dir string, stdin io.Reader,
	args ...string) error {

	base := []string{"-loglevel", "error", "-y"}
	if stdin == nil {
		// ffmpeg reads keys from a terminal stdin, and stalls on them
		base = append(base, "-nostdin")
	}

	// Nothing here should take this long: a hung ffmpeg (bad input
	// under -loop 1 retries forever) must not hang the build
	ctx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg", append(base, args...)...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = stdin
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

	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("%s: no duration (empty?)", path)
	}
	return secs, nil
}

// hasAudio reports whether a media file has a sound stream.
func hasAudio(ctx context.Context, path string) (bool, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-select_streams", "a", "-show_entries", "stream=index",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		return false, fmt.Errorf("ffprobe %s: %w", path, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// probeSize is a video's (first video stream's) frame size.
func probeSize(ctx context.Context, path string) (w, h int, err error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-select_streams", "v:0", "-show_entries", "stream=width,height",
		"-of", "csv=p=0:s=x", path).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("ffprobe %s: %w", path, err)
	}

	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%dx%d", &w,
		&h); err != nil {
		return 0, 0, fmt.Errorf("%s: no video size: %w", path, err)
	}
	return w, h, nil
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
