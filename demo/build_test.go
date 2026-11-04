package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestSceneLength(t *testing.T) {
	// Narration + lead + tail, rounded up to whole frames
	got := sceneLength(3.01, 0)
	if want := math.Ceil((leadIn+3.01+tailOut)*fps) / fps; got != want {
		t.Fatalf("sceneLength = %v, want %v", got, want)
	}
	if frames := got * fps; math.Abs(frames-math.Round(frames)) > 1e-9 {
		t.Fatalf("%v s is not whole frames", got)
	}

	// Silent scenes take Secs; Secs is a floor for spoken ones
	if got := sceneLength(0, 2); got != 2 {
		t.Fatalf("silent: %v", got)
	}
	if got := sceneLength(1, 5); got != 5 {
		t.Fatalf("floor: %v", got)
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
		{"secs": 2, "diff": {"args": ["HEAD~1"]}}]}`), 0o644)

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
