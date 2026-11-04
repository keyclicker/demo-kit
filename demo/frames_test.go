package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestFrameTimes(t *testing.T) {
	// A 37 s video at 2 s steps stops at 35: 37 would be its very end,
	// where a seek finds no frame
	got := frameTimes(37.001, 0)
	if len(got) != 18 || got[0] != 1 || got[len(got)-1] != 35 {
		t.Fatalf("37 s: %v", got)
	}

	if got := frameTimes(10, 4); !slices.Equal(got, []float64{2, 6}) {
		t.Fatalf("every 4: %v", got)
	}
}

func TestSheetPath(t *testing.T) {
	// A demo is named for its folder, any other video for itself
	for video, want := range map[string]string{
		"/d/demo-kit-zoom/demo.mp4":         "demo-kit-zoom-frames.png",
		"/t/demo-kit-zoom-preview.mp4":      "demo-kit-zoom-preview-frames.png",
		"/d/demo-kit-zoom/artifacts/ui.mp4": "ui-frames.png",
	} {
		path, err := sheetPath(video)
		if err != nil || filepath.Base(path) != want {
			t.Errorf("%s: %s %v, want %s", video, path, err, want)
		}
	}
}
