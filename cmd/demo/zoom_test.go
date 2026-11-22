package main

import (
	"os/exec"
	"strconv"
	"testing"
)

func TestZoomView(t *testing.T) {
	view := func(box []int, w, h int) rect {
		t.Helper()
		r, err := fitRegion(box, w, h)
		if err != nil {
			t.Fatalf("%v: %v", box, err)
		}
		return zoomView(r)
	}

	// A wide region grows to 4:3 around its center
	if r := view([]int{400, 400, 640, 100}, 1280, 960); r !=
		(rect{400, 210, 640, 480}) {
		t.Fatalf("wide: %+v", r)
	}

	// A tiny one stops at maxZoom; at the edge it stays inside
	if r := view([]int{0, 0, 10, 10}, 1280, 960); r !=
		(rect{0, 0, frameW / maxZoom, frameH / maxZoom}) {
		t.Fatalf("tiny: %+v", r)
	}

	// Through the fit: 1920x1080 is letterboxed, 120 px bars
	if r := view([]int{0, 0, 960, 540}, 1920, 1080); r !=
		(rect{0, 60, 640, 480}) {
		t.Fatalf("letterboxed: %+v", r)
	}

	if _, err := fitRegion([]int{1000, 0, 400, 10}, 1280, 960); err == nil {
		t.Fatal("outside the clip: accepted")
	}
}

func TestZoomFilter(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}

	// A 256 px wide ramp, its value its x: zoomed to the left half from
	// frame 2, eased over the 4 frames that step has
	const n = 6
	views := []rect{fullFrame, {0, 0, frameW / 2, frameH / 2}}
	vf := "format=gray,geq=lum='X'," + zoomFilter(views, []int{0, 2}, n)
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i",
		"color=s=256x8:r=30", "-vf", vf, "-frames:v", strconv.Itoa(n), "-f",
		"rawvideo", "-pix_fmt", "gray", "-").Output()
	if err, ok := err.(*exec.ExitError); ok {
		t.Fatalf("ffmpeg: %s", err.Stderr)
	}
	if err != nil || len(out) != n*256*8 {
		t.Fatalf("ffmpeg: %d bytes, %v", len(out), err)
	}

	// The right edge: all of it, still all of it at the cut, then halfway
	// through the ease (smoothstep 0.5: a quarter of the way in)
	for f, want := range map[int]int{0: 255, 2: 255, 4: 191} {
		if got := int(out[f*256*8+255]); got < want-3 || got > want+3 {
			t.Errorf("frame %d: right edge %d, want %d", f, got, want)
		}
	}
}
