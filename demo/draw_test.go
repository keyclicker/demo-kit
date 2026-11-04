package main

import (
	"context"
	"image"
	"path/filepath"
	"strings"
	"testing"
)

// collect runs shots and returns what they emitted.
func collect(t *testing.T, s shots) []shot {
	t.Helper()
	var out []shot
	err := s(func(sh shot) error {
		out = append(out, sh)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// total is the frames shots cover.
func total(shots []shot) int {
	n := 0
	for _, s := range shots {
		n += s.n
	}
	return n
}

func TestStill(t *testing.T) {
	// Fits: one shot. Taller: pages, flush with the bottom at the end
	if got := collect(t, still(newCanvas(frameH), 90, false)); len(got) != 1 {
		t.Fatalf("one frame: %d shots", len(got))
	}

	tall := newCanvas(frameH + pageStep + 1)
	pages := collect(t, still(tall, 91, false))
	if len(pages) != 3 || total(pages) != 91 {
		t.Fatalf("pages: %d shots, %d frames", len(pages), total(pages))
	}
	if last := pages[2]; last.y1 != pageStep+1 {
		t.Fatalf("last page at %d", last.y1)
	}

	// Smooth: hold, then one pan to the bottom
	pan := collect(t, still(tall, 120, true))
	if len(pan) != 2 || total(pan) != 120 || pan[1].y1 != pageStep+1 ||
		pan[1].move != 120-2*fps {
		t.Fatalf("pan: %+v", pan)
	}
}

func TestStepShots(t *testing.T) {
	img := newCanvas(3 * frameH)
	ys := []int{500, -1, 100}
	got := collect(t, stepShots([]int{0, 30, 50}, 80, true,
		func(k int) (stepView, error) { return stepView{img, ys[k]}, nil }))

	if len(got) != 3 || total(got) != 80 || got[0].n != 30 {
		t.Fatalf("shots: %+v", got)
	}
	// No y: stay; smooth: ease from the last view
	if got[0].y0 != 500 || got[1].y1 != 500 || got[2].y0 != 500 ||
		got[2].y1 != 100 || got[2].move != frameCount(scrollSecs) {
		t.Fatalf("views: %+v", got)
	}
}

func TestView(t *testing.T) {
	img := newCanvas(2 * frameH)
	s := shot{img: img, y0: 0, y1: 100, move: 10, n: 20}

	top := func(f int) int {
		b := view(s, f)
		return (len(img.Pix) - cap(b)) / img.Stride // rows above the view
	}
	if len(view(s, 0)) != frameW*frameH*4 {
		t.Fatal("a view isn't one frame")
	}
	// Eases from y0 to y1, then holds
	if top(0) != 0 || top(5) != 50 || top(10) != 100 || top(19) != 100 {
		t.Fatalf("eased: %d %d %d %d", top(0), top(5), top(10), top(19))
	}
}

func TestSpotlight(t *testing.T) {
	// A 640x480 source fits the frame at 2x
	p := &picture{canvas: newCanvas(frameH), src: image.Rect(0, 0, 640, 480),
		scale: 2}
	fill(p.canvas, 0, 0, frameW, frameH, colText)

	v, err := p.spotlight([]int{10, 10, 100, 50})
	if err != nil || v.img == p.canvas || v.y != 0 {
		t.Fatalf("spotlight: %v %v", v.y, err)
	}
	if _, err := p.spotlight([]int{600, 0, 100, 50}); err == nil {
		t.Fatal("box outside the image: accepted")
	}

	// Outside the box fades, inside stays
	if c := v.img.RGBAAt(400, 400); c == colText {
		t.Fatal("outside not faded")
	}
	if c := v.img.RGBAAt(100, 50); c != colText {
		t.Fatalf("inside changed: %v", c)
	}
}

func TestCenterY(t *testing.T) {
	if y := centerY(1000, 1100, 3000); y != 1050-frameH/2 {
		t.Fatalf("center: %d", y)
	}
	if y := centerY(10, 20, 3000); y != 0 {
		t.Fatalf("clamped top: %d", y)
	}
	if y := centerY(2950, 3000, 3000); y != 3000-frameH {
		t.Fatalf("clamped bottom: %d", y)
	}
	if y := centerY(500, 500+frameH, 3000); y != 500-margin {
		t.Fatalf("tall span shows its top: %d", y)
	}
}

func TestSlideCheck(t *testing.T) {
	if err := (SlideSpec{Title: "a", Text: "b"}).check(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.Repeat("y,", 40), ",")
	long := SlideSpec{End: "x", Lines: lines}
	if err := long.check(); err == nil {
		t.Fatal("overfull slide: accepted")
	}
}

func TestDuckVolume(t *testing.T) {
	got := duckVolume(timing{Starts: []float64{1}, Ends: []float64{2}})
	want := "1-0.750*max(0,clip((t-0.850)/0.150,0,1)*clip((2.400-t)/0.400,0,1))"
	if got != want {
		t.Fatalf("duckVolume = %s", got)
	}
}

func TestParseANSI(t *testing.T) {
	lines := parseANSI("\x1b]0;title\x07\x1b[1;32mok\x1b[0m done\r\n" +
		"10%\r50%\r\n\tx\x1b[38;5;196my")
	if len(lines) != 3 {
		t.Fatalf("%d lines: %+v", len(lines), lines)
	}

	ok := lines[0][0]
	if ok.text != "ok" || !ok.bold || ok.col != ansiColors[2] ||
		lines[0][1].text != " done" || lines[0][1].bold {
		t.Fatalf("styles: %+v", lines[0])
	}
	if lines[1][0].text != "50%" {
		t.Fatalf("\\r redraws its line: %+v", lines[1])
	}
	if lines[2][0].text != "        x" || lines[2][1].col != color256(196) {
		t.Fatalf("tab, 256 colors: %+v", lines[2])
	}
}

func TestPrefixRuns(t *testing.T) {
	lines := [][]run{{{text: "ab"}, {text: "cd"}}, {{text: "ef"}}}
	got := prefixRuns(lines, 3)
	if len(got) != 1 || len(got[0]) != 2 || got[0][1].text != "c" {
		t.Fatalf("prefix 3: %+v", got)
	}
	// The line break counts as one
	if got := prefixRuns(lines, 6); len(got) != 2 || got[1][0].text != "e" {
		t.Fatalf("prefix 6: %+v", got)
	}
}

func TestRunTerm(t *testing.T) {
	spec := TermSpec{Run: []string{"cd /", "pwd; echo hi >&2", "false"},
		Dir: t.TempDir()}
	got, err := runTerm(context.Background(), spec,
		filepath.Join(t.TempDir(), "term.sh"))
	if err != nil {
		t.Fatal(err)
	}

	// One shell: cd sticks; stderr shows; statuses kept
	if len(got) != 3 || got[1].Output != "/\r\nhi\r\n" || got[2].Status != 1 {
		t.Fatalf("results: %+v", got)
	}
}
