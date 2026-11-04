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

	// A scene's voice goes over the script's; steps fit any picture
	os.WriteFile(path, []byte(`{"voice": {"name": "Charon"}, "scenes": [
		{"say": "a", "slide": {"title": "T"}, "voice": {"tempo": 1.5}},
		{"say": "b", "compare": [{"image": "x.png"}, {"image": "y.png"}]},
		{"clip": "a.mp4", "audio": true, "steps": [{"say": "c"}]},
		{"image": "i.png", "steps": [{"say": "d", "box": [1, 2, 3, 4]}]},
		{"term": {"run": ["ls", "pwd"]}, "steps": [{"say": "e"}, {"say": "f"}]}]}`),
		0o644)
	if s, err = loadScript(path); err != nil {
		t.Fatal(err)
	}
	if v := s.Scenes[0].Voice; v.Name != "Charon" || v.Tempo != 1.5 ||
		v.Model != defaultVoice.Model {
		t.Fatalf("scene voice: %+v", v)
	}
	if v := s.Scenes[1].Voice; v.Tempo != defaultVoice.Tempo {
		t.Fatalf("inherited voice: %+v", v)
	}
	if s.Scenes[1].Compare[0].Image != filepath.Join(dir, "x.png") {
		t.Fatalf("pane not resolved: %s", s.Scenes[1].Compare[0].Image)
	}
	if s.Scenes[2].decimate() {
		t.Fatal("audio kept decimate on")
	}

	bad := map[string]string{
		"two sources": `{"scenes": [{"say": "x", "clip": "a", "image": "b"}]}`,
		"say + steps": `{"scenes": [{"say": "x", "code": {"file": "a"}, "steps": [{"say": "y"}]}]}`,
		"clip focus":  `{"scenes": [{"clip": "a", "steps": [{"say": "y", "focus": [1, 2]}]}]}`,
		"code box":    `{"scenes": [{"code": {"file": "a"}, "steps": [{"say": "y", "box": [0, 0, 5, 5]}]}]}`,
		"bad box":     `{"scenes": [{"image": "a", "steps": [{"say": "y", "box": [0, 0, 5]}]}]}`,
		"image from":  `{"scenes": [{"say": "x", "image": "a", "from": 2}]}`,
		"audio + dec": `{"scenes": [{"say": "x", "clip": "a", "audio": true, "decimate": true}]}`,
		"term steps":  `{"scenes": [{"term": {"run": ["a", "b"]}, "steps": [{"say": "y"}]}]}`,
		"term empty":  `{"scenes": [{"say": "x", "term": {"run": []}}]}`,
		"two slides":  `{"scenes": [{"say": "x", "slide": {"title": "a", "end": "b"}}]}`,
		"one pane":    `{"scenes": [{"say": "x", "compare": [{"image": "a"}]}]}`,
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

func TestPageCount(t *testing.T) {
	for tall, want := range map[int]int{frameH: 1, frameH + 1: 2,
		frameH + pageStep: 2, frameH + pageStep + 1: 3} {
		if got := pageCount(tall); got != want {
			t.Errorf("pageCount(%d) = %d, want %d", tall, got, want)
		}
	}
}
