package main

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
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

// Only a silent clip takes its cut's length; narration sets the rest
func TestMinLength(t *testing.T) {
	cut := span{from: 2, to: 14}

	if l := (Scene{Clip: "a.mp4"}).minLength(cut); l != 12 {
		t.Fatalf("silent clip: %v, want the cut's 12 s", l)
	}
	if l := (Scene{Clip: "a.mp4", Say: "hi"}).minLength(cut); l != 0 {
		t.Fatalf("narrated clip: %v, want no floor", l)
	}
	steps := []Step{{Say: "a"}, {Say: "b"}}
	if l := (Scene{Clip: "a.mp4", Steps: steps}).minLength(cut); l != 0 {
		t.Fatalf("narrated steps: %v, want no floor", l)
	}
	if l := (Scene{Clip: "a.mp4", Say: "hi", Secs: 9}).minLength(cut); l != 9 {
		t.Fatalf("secs: %v, want 9", l)
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
		{"say": "b", "image": "x.png"},
		{"clip": "a.mp4", "audio": true, "steps": [{"say": "c"}]},
		{"image": "i.png", "steps": [{"say": "d", "box": [1, 2, 3, 4]}]},
		{"clip": "a.mp4", "steps": [{"say": "g", "zoom": [1, 2, 3, 4]}]},
		{"image": "i.png", "steps": [{"say": "h", "box": [1, 2, 3, 4],
			"zoom": [0, 0, 9, 9]}]},
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
	if s.Scenes[1].Image != filepath.Join(dir, "x.png") {
		t.Fatalf("image not resolved: %s", s.Scenes[1].Image)
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
		"term zoom":   `{"scenes": [{"term": {"run": ["a"]}, "steps": [{"say": "y", "zoom": [0, 0, 5, 5]}]}]}`,
		"clip box":    `{"scenes": [{"clip": "a", "steps": [{"say": "y", "box": [0, 0, 5, 5]}]}]}`,
		"bad align":   `{"scenes": [{"say": "y", "code": {"file": "a", "align": "middle"}}]}`,
		"bad focus":   `{"scenes": [{"code": {"file": "a"}, "steps": [{"say": "y", "focus": [3]}]}]}`,
		"empty mark":  `{"scenes": [{"code": {"file": "a"}, "steps": [{"say": "y", "marks": [""]}]}]}`,
		"no length":   `{"scenes": [{"slide": {"title": "a"}}]}`,
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

func TestFinalFilter(t *testing.T) {
	// libass refuses an empty captions file: a silent video has none
	if vf := finalFilter(false); strings.Contains(vf, "subtitles") {
		t.Fatalf("no captions: %s", vf)
	}
	if vf := finalFilter(true); !strings.Contains(vf, "subtitles=") {
		t.Fatalf("captions: %s", vf)
	}
}

func TestUncached(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	v := defaultVoice
	scenes := []Scene{{Say: "cached", Voice: &v}, {Secs: 1, Voice: &v}}

	wav, err := cachePath(v, "cached")
	if err != nil {
		t.Fatal(err)
	}
	if !uncached(scenes) {
		t.Fatal("missing line: cached")
	}

	os.WriteFile(wav, nil, 0o644)
	if uncached(scenes) {
		t.Fatal("cached line: needs the key")
	}
}

func TestClipPastEnd(t *testing.T) {
	clip := lavfiClip(t, "testsrc=d=1:s=64x48:r=30")
	s := Scene{Clip: clip, From: 5, Secs: 1, num: 1}
	b := newBuilder(&Script{Scenes: []Scene{s}}, t.TempDir(), true)

	err := b.cutClips(context.Background())
	if err == nil || !strings.Contains(err.Error(), "past the clip's end") {
		t.Fatalf("%v", err)
	}
}

// A clip with nothing said plays its own cut, every frame
func TestSilentClipLength(t *testing.T) {
	clip := lavfiClip(t, "testsrc=d=3:s=64x48:r=30")
	s := Scene{Clip: clip, From: 1, num: 1}
	if err := s.check(); err != nil {
		t.Fatal(err)
	}
	if s.decimate() {
		t.Fatal("nothing said, yet idle frames drop")
	}

	b := newBuilder(&Script{Scenes: []Scene{s}}, t.TempDir(), true)
	ctx := context.Background()
	if err := b.cutClips(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.speak(ctx); err != nil {
		t.Fatal(err)
	}
	if l := b.timings[0].Length; math.Abs(l-2) > 0.05 {
		t.Fatalf("length %.2f, want the cut's 2 s", l)
	}
}
