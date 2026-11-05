package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lavfiClip writes a clip made of ffmpeg lavfi inputs (each "-f lavfi
// -i" source) to the test's temp dir, or skips without ffmpeg.
func lavfiClip(t *testing.T, sources ...string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}

	path := filepath.Join(t.TempDir(), "clip.mp4")
	args := []string{"-v", "error"}
	for _, src := range sources {
		args = append(args, "-f", "lavfi", "-i", src)
	}
	args = append(args, "-c:v", "libx264", "-shortest", path)
	out, err := exec.Command("ffmpeg", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}
	return path
}

func TestParsePauses(t *testing.T) {
	log := "[silencedetect] silence_start: 2.4\n" +
		"[silencedetect] silence_end: 3.4 | silence_duration: 1\n" +
		"[silencedetect] silence_start: 9.5\n"
	ps := parsePauses(log, 10)
	if len(ps) != 2 || ps[0] != (span{2.4, 3.4}) || ps[1] != (span{9.5, 10}) {
		t.Fatalf("%v", ps)
	}
}

func TestCutAt(t *testing.T) {
	// Speech, a pause at 4-5, speech, a pause at 8-8.2, speech
	ps := []span{{4, 5}, {8, 8.2}}

	cases := []struct {
		t   float64
		end bool
		at  float64
		ok  bool
	}{
		{4.5, true, 4.5, true},   // already in a pause
		{6, true, 8.1, true},     // an end finishes its phrase
		{6, false, 4.75, true},   // a start begins its phrase
		{10, true, 8.1, true},    // nothing after: back to the last
		{1.5, false, 4.75, true}, // nothing before: on to the next
		{20, true, 20, false},    // no pause in reach
	}
	for _, c := range cases {
		at, ok := cutAt(ps, c.t, c.end)
		if at != c.at || ok != c.ok {
			t.Errorf("cutAt(%g, end %v) = %g %v, want %g %v", c.t, c.end,
				at, ok, c.at, c.ok)
		}
	}
}

func TestSlideShare(t *testing.T) {
	scenes := []Scene{{Slide: &SlideSpec{}}, {Code: &CodeSpec{}},
		{Slide: &SlideSpec{}}}
	timings := []timing{{Length: 3}, {Length: 6}, {Length: 3}}
	if got := slideShare(scenes, timings); got != 0.5 {
		t.Fatalf("%g", got)
	}

	// Markdown shown as code reads like slides too
	scenes[1].Code.File = "notes.MD"
	if got := slideShare(scenes, timings); got != 1 {
		t.Fatalf("markdown: %g", got)
	}
}

func TestQuietCutsCross(t *testing.T) {
	// Speech, a pause at 5-6, speech: from 3 moves on to 5.75, to 4 to
	// 5.25. Crossed, the script's own cuts stay
	clip := lavfiClip(t, "testsrc=d=10:s=64x48:r=30",
		"sine=d=10,volume='if(between(t,5,6),0,1)':eval=frame")
	from, to, err := quietCuts(context.Background(),
		Scene{Clip: clip, Audio: true, From: 3, To: 4, num: 1})
	if err != nil || from != 3 || to != 4 {
		t.Fatalf("cuts %g-%g, %v; want the script's 3-4", from, to, err)
	}
}

func TestQuietCutsNoSound(t *testing.T) {
	clip := lavfiClip(t, "testsrc=d=1:s=64x48:r=30")
	_, _, err := quietCuts(context.Background(),
		Scene{Clip: clip, Audio: true, num: 1})
	if err == nil || !strings.Contains(err.Error(), "no sound") {
		t.Fatalf("%v", err)
	}
}
