package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestSceneTiming(t *testing.T) {
	// Lead, steps with gaps, tail; whole frames
	got := sceneTiming([]float64{2, 3.01}, 0)
	if got.Starts[0] != leadIn || got.Starts[1] != leadIn+2+stepGap {
		t.Fatalf("starts %v", got.Starts)
	}
	if got.Cuts[0] != 0 || got.Cuts[1] != frames(leadIn+2+stepGap/2) {
		t.Fatalf("cuts %v", got.Cuts)
	}
	want := math.Ceil((leadIn+2+stepGap+3.01+tailOut)*fps) / fps
	if got.Length != want {
		t.Fatalf("length %v, want %v", got.Length, want)
	}
	if frames := got.Length * fps; math.Abs(frames-math.Round(frames)) > 1e-9 {
		t.Fatalf("%v s is not whole frames", got.Length)
	}

	// Silent scenes take secs; secs is a floor for spoken ones
	if got := sceneTiming(nil, 2); got.Length != 2 {
		t.Fatalf("silent: %v", got.Length)
	}
	if got := sceneTiming([]float64{1}, 5); got.Length != 5 {
		t.Fatalf("floor: %v", got.Length)
	}
}

func TestSpeedFor(t *testing.T) {
	if s := speedFor(20, 5); s != 4 {
		t.Fatalf("long clip: %v", s)
	}
	if s := speedFor(2, 5); s != 1 {
		t.Fatalf("short clip slowed down: %v", s)
	}
}

func TestLoadScript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	os.WriteFile(path, []byte(`{"title": "T", "scenes": [
		{"say": "hi", "clip": "a.mp4", "from": 1, "to": 2},
		{"code": {"diff": ["HEAD~1"]}, "steps": [
			{"say": "a", "focus": [1, 4]}, {"say": "b"}]}]}`), 0o644)

	s, err := loadScript(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Scenes[0].Clip != filepath.Join(dir, "a.mp4") {
		t.Fatalf("clip not resolved: %s", s.Scenes[0].Clip)
	}
	if s.Voice.Model != defaultVoice.Model {
		t.Fatalf("voice defaults missing: %+v", s.Voice)
	}

	bad := map[string]string{
		"two sources": `{"scenes": [{"say": "x", "clip": "a", "image": "b"}]}`,
		"say + steps": `{"scenes": [{"say": "x", "code": {"file": "a"}, "steps": [{"say": "y"}]}]}`,
		"clip steps":  `{"scenes": [{"clip": "a", "steps": [{"say": "y"}]}]}`,
		"bad focus":   `{"scenes": [{"code": {"file": "a"}, "steps": [{"say": "y", "focus": [3]}]}]}`,
		"empty mark":  `{"scenes": [{"code": {"file": "a"}, "steps": [{"say": "y", "marks": [""]}]}]}`,
		"bad scroll":  `{"scenes": [{"say": "y", "code": {"file": "a", "scroll": "fast"}}]}`,
		"no length":   `{"scenes": [{"clip": "a"}]}`,
		"typo":        `{"scenes": [{"say": "x", "clp": "a"}]}`,
		"empty":       `{"scenes": []}`,
	}
	for name, js := range bad {
		os.WriteFile(path, []byte(js), 0o644)
		if _, err := loadScript(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestConcatListQuotes(t *testing.T) {
	got := concatList([]string{"/a/it's.mp4"})
	if want := "file '/a/it'\\''s.mp4'\n"; got != want {
		t.Fatalf("concatList = %q", got)
	}
}

func TestPagedY(t *testing.T) {
	// Fits: one page, stays at 0. Taller: pages pageStep apart
	if got := pagedY(frameH, 6); got != "min(floor(t/6.000)*864,ih-oh)" {
		t.Fatalf("one page: %s", got)
	}
	if got := pagedY(frameH+pageStep+1, 6); got !=
		"min(floor(t/2.000)*864,ih-oh)" {
		t.Fatalf("three pages: %s", got)
	}
}
