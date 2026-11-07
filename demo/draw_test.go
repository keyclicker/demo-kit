package main

import (
	"context"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
	if got := collect(t, still(newCanvas(frameH), nil, 90)); len(got) != 1 {
		t.Fatalf("one frame: %d shots", len(got))
	}

	tall := newCanvas(frameH + pageStep + 1)
	pages := collect(t, still(tall, nil, 91))
	if len(pages) != 3 || total(pages) != 91 {
		t.Fatalf("pages: %d shots, %d frames", len(pages), total(pages))
	}
	if last := pages[2]; last.y != pageStep+1 {
		t.Fatalf("last page at %d", last.y)
	}
}

func TestStepShots(t *testing.T) {
	img := newCanvas(3 * frameH)
	ys := []int{500, -1, 100}
	got := collect(t, stepShots([]int{0, 30, 50}, 80,
		func(k int) (stepView, error) { return stepView{img, nil, ys[k]}, nil }))

	if len(got) != 3 || total(got) != 80 || got[0].n != 30 {
		t.Fatalf("shots: %+v", got)
	}
	// No y: stay where the last step was
	if got[0].y != 500 || got[1].y != 500 || got[2].y != 100 {
		t.Fatalf("views: %+v", got)
	}
}

func TestView(t *testing.T) {
	img := newCanvas(2 * frameH)
	b := view(shot{img: img, y: 100, n: 20})
	if len(b) != frameW*frameH*4 {
		t.Fatal("a view isn't one frame")
	}
	if top := (len(img.Pix) - cap(b)) / img.Stride; top != 100 {
		t.Fatalf("view at row %d", top)
	}
}

func TestPinned(t *testing.T) {
	heads := []int{margin, 2000}
	for _, tc := range []struct {
		y, head, at int
		ok          bool
	}{
		{0, 0, 0, false},                                   // at rest: the real one
		{500, margin, margin, true},                        // scrolled past: pinned
		{2000 - 2*margin - fileRowH, margin, margin, true}, // next touches
		{2000 - margin - fileRowH, margin, 0, true},        // pushed up
		{2000 - margin, 0, 0, false},                       // the next at rest
		{2100, 2000, margin, true},                         // the next pinned
	} {
		head, at, ok := pinned(heads, tc.y)
		if ok != tc.ok || ok && (head != tc.head || at != tc.at) {
			t.Errorf("y %d: %d %d %v, want %d %d %v", tc.y, head, at, ok,
				tc.head, tc.at, tc.ok)
		}
	}
}

// Scrolled down, the frame's top shows the header, not the code there
func TestViewPinsHeader(t *testing.T) {
	img := newCanvas(3 * frameH)
	fill(img, 0, frameH, frameW, 2*frameH, colText) // "code" below
	fill(img, 0, margin, frameW, fileRowH, colFileBG)

	b := view(shot{img: img, heads: []int{margin}, y: frameH, n: 1})
	px := func(row int) color.RGBA {
		i := row * img.Stride
		return color.RGBA{b[i], b[i+1], b[i+2], b[i+3]}
	}
	if px(0) != colBG || px(margin) != colFileBG ||
		px(margin+fileRowH) != colText {
		t.Fatalf("rows: %v %v %v", px(0), px(margin), px(margin+fileRowH))
	}
	if img.RGBAAt(0, frameH) != colText {
		t.Fatal("pinning drew on the picture")
	}
}

func TestSpotlight(t *testing.T) {
	// A 640x480 source fits the frame at 2x
	p := &picture{canvas: newCanvas(frameH), src: image.Rect(0, 0, 640, 480),
		scale: 2}
	fill(p.canvas, 0, 0, frameW, frameH, colText)

	point := func(st Step) (aim, stepView) {
		t.Helper()
		a, err := p.aim(st)
		if err != nil {
			t.Fatal(err)
		}
		return a, p.point(a)
	}

	a, v := point(Step{Box: []int{10, 10, 100, 50}})
	if v.img == p.canvas || v.y != 0 || a.view != fullFrame {
		t.Fatalf("spotlight: y %d, view %+v", v.y, a.view)
	}
	if _, err := p.aim(Step{Box: []int{600, 0, 100, 50}}); err == nil {
		t.Fatal("box outside the image: accepted")
	}

	// Outside the box fades, inside stays
	if c := v.img.RGBAAt(400, 400); c == colText {
		t.Fatal("outside not faded")
	}
	if c := v.img.RGBAAt(100, 50); c != colText {
		t.Fatalf("inside changed: %v", c)
	}

	// A box at the edge keeps its whole outline, inside the picture
	_, v = point(Step{Box: []int{0, 0, 100, 50}})
	if c := v.img.RGBAAt(0, 40); c != colFocus || v.img.RGBAAt(2, 40) !=
		colFocus {
		t.Fatalf("left outline at the edge: %v", c)
	}

	// A zoom alone: the picture plain, a small region up to maxZoom
	a, v = point(Step{Zoom: []int{0, 0, 100, 50}})
	if v.img != p.canvas || a.view != (rect{0, 0, frameW / maxZoom,
		frameH / maxZoom}) {
		t.Fatalf("zoom alone: %+v", a.view)
	}

	// Both: the outline thinner by the zoom, so it's as thick on screen
	_, v = point(Step{Box: []int{0, 0, 100, 50}, Zoom: []int{0, 0, 100, 50}})
	if c := v.img.RGBAAt(1, 40); c == colFocus {
		t.Fatal("outline not thinned for a 4x zoom")
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

	// One word wider than the frame doesn't wrap: it's rejected too
	wide := SlideSpec{Callout: "github.com/keyclicker/demo-kit"}
	if err := wide.check(); err == nil {
		t.Fatal("too wide slide: accepted")
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

	// A program's own CRLF reaches us as \r\r\n (the pty's onlcr)
	lines = parseANSI("HTTP/1.1 200 OK\r\r\nX: y\r\r\n")
	var texts []string
	for _, l := range lines {
		var text string
		for _, r := range l {
			text += r.text
		}
		texts = append(texts, text)
	}
	if !slices.Equal(texts, []string{"HTTP/1.1 200 OK", "X: y"}) {
		t.Fatalf("CRLF: %q", texts)
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

func TestRunTermMarkerBytes(t *testing.T) {
	// Output with the markers' control bytes can't split a session
	spec := TermSpec{Run: []string{`printf 'a\x1eb\x1fc\n'`, "echo two"},
		Dir: t.TempDir()}
	got, err := runTerm(context.Background(), spec,
		filepath.Join(t.TempDir(), "term.sh"))
	if err != nil || len(got) != 2 || got[1].Output != "two\r\n" {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestRunTermCanceled(t *testing.T) {
	// A canceled build takes the session's commands down with it
	dir := t.TempDir()
	spec := TermSpec{Run: []string{`sh -c 'echo $$ > pid; exec sleep 300'`},
		Dir: dir}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if _, err := runTerm(ctx, spec, filepath.Join(dir, "term.sh")); err == nil {
		t.Fatal("canceled session: no error")
	}

	data, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	for range 30 {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("pid %d still running", pid)
}

func TestRunSessionsInOrder(t *testing.T) {
	// Scene 2 reads what scene 1 makes, however slowly
	dir := t.TempDir()
	scenes := []Scene{
		{Term: &TermSpec{Run: []string{"sleep 0.5; echo made > f"},
			Dir: dir}, Secs: 1, num: 1},
		{Term: &TermSpec{Run: []string{"cat f"}, Dir: dir}, Secs: 1, num: 2},
	}
	b := newBuilder(&Script{Scenes: scenes}, t.TempDir(), true)

	if err := b.runSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := b.sessions[1].results[0]; got.Status != 0 ||
		got.Output != "made\r\n" {
		t.Fatalf("scene 2: %+v", got)
	}
}

// Characters the font lacks are named once; the rest pass quietly
func TestWarnGlyphs(t *testing.T) {
	var out strings.Builder
	warnGlyphs(&out, "scene 3", []string{"sound 🔊 on", "🔊\r│ café"})
	want := `demo: scene 3: no glyph for "🔊": it draws as a box` + "\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}

	out.Reset()
	warnGlyphs(&out, "code", []string{"plain ├── ascii…"})
	if out.Len() != 0 {
		t.Fatalf("warned on drawable text: %q", out.String())
	}
}
