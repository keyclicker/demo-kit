package main

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"
)

func TestParseChanges(t *testing.T) {
	log := "n:   0 pts:      0 pts_time:0       duration\n" +
		"n:   1 pts:  30720 pts_time:2       duration\n"
	if got := parseChanges(log, 10); !slices.Equal(got, []float64{10, 12}) {
		t.Fatalf("%v", got)
	}
}

func TestTrimStill(t *testing.T) {
	c := span{0, 10}
	for _, tc := range []struct {
		name  string
		kept  []float64
		quiet []span
		want  span
		ok    bool
	}{
		{"silent clip: still edges go", []float64{0, 2, 3, 6},
			[]span{c}, span{1.5, 6.5}, true},
		{"moves at once: holds no lead", []float64{0, 0.1, 9.9},
			[]span{c}, span{0, 10}, true},
		{"never moves", []float64{0}, []span{c}, c, false},
		{"sound before motion keeps it", []float64{0, 2, 6},
			[]span{{0, 1}, {8, 10}}, span{0.5, 8.5}, true},
		{"sound at both edges: no trim", []float64{0, 2, 6},
			nil, c, true},
		{"sound alone in a still clip", []float64{0},
			[]span{{0, 3}, {5, 10}}, span{2.5, 5.5}, true},
	} {
		got, ok := trimStill(c, tc.kept, tc.quiet)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: %v %v, want %v %v", tc.name, got, ok, tc.want,
				tc.ok)
		}
	}
}

func TestDeadAir(t *testing.T) {
	ss := stills([]float64{0, 1, 5}, 9)
	if !slices.Equal(ss, []span{{0, 1}, {1, 5}, {5, 9}}) {
		t.Fatalf("stills %v", ss)
	}

	// Still 1-5 and 5-9; quiet 2-4.5 (2.5 s) and 8-9 (too short)
	dead := deadAir(ss, []span{{2, 4.5}, {8, 9}})
	if !slices.Equal(dead, []span{{2, 4.5}}) {
		t.Fatalf("dead %v", dead)
	}
}

// A real clip: still 2 s, moving 2 s, still 3 s
func TestTrimCut(t *testing.T) {
	clip := lavfiClip(t, "testsrc=d=2:s=64x48:r=30,tpad=start_mode=clone:"+
		"start_duration=2:stop_mode=clone:stop_duration=3")
	var log strings.Builder
	s := Scene{Clip: clip, num: 3}

	got, err := trimCut(context.Background(), &log, s, span{0, 7})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.from-1.5) > 0.1 || math.Abs(got.to-4.5) > 0.1 {
		t.Fatalf("cut %v, want about 1.5-4.5", got)
	}
	if !strings.Contains(log.String(), "scene 3: still edges trimmed") {
		t.Fatalf("log %q", log.String())
	}
}

// A real video: moving 1 s, then still and silent 3 s
func TestWarnDeadAir(t *testing.T) {
	video := lavfiClip(t, "testsrc=d=1:s=64x48:r=30,tpad=stop_mode=clone:"+
		"stop_duration=3", "anullsrc=r=48000:cl=mono:d=4")
	b := &builder{script: &Script{Scenes: []Scene{{num: 1}, {num: 2}}},
		timings: []timing{{Length: 0.5}, {Length: 3.5}}}
	var log strings.Builder

	if err := b.warnDeadAir(context.Background(), &log, video); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "dead air 0:01.0-0:04.0 (3.0 s, "+
		"scene 2)") {
		t.Fatalf("log %q", log.String())
	}
}

// Past the audio's own end is quiet: a still tail after the sound
// stops is trimmed, and a clip still and silent throughout is nothing.
func TestTrimCutAudioEndsFirst(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources []string
		cut     span
		want    span
		ok      bool
	}{
		{"still tail after the sound", []string{"testsrc=d=1:s=64x48:" +
			"r=30,tpad=stop_mode=clone:stop_duration=3",
			"sine=d=1,volume='if(gt(t,0.6),0,1)':eval=frame"},
			span{0, 4}, span{0, 1.5}, true},
		{"still and silent", []string{"testsrc=d=0.04:s=64x48:r=30," +
			"tpad=stop_mode=clone:stop_duration=4",
			"anullsrc=r=48000:cl=mono:d=3.8"}, span{0, 4.04},
			span{0, 4.04}, false},
	} {
		clip := lavfiClip(t, tc.sources...)
		var log strings.Builder
		s := Scene{Clip: clip, Audio: true, num: 2}

		got, err := trimCut(context.Background(), &log, s, tc.cut)
		if err != nil {
			t.Fatal(err)
		}

		ok := !strings.Contains(log.String(), "nothing moves")
		if ok != tc.ok || math.Abs(got.from-tc.want.from) > 0.1 ||
			math.Abs(got.to-tc.want.to) > 0.1 {
			t.Errorf("%s: cut %v, log %q; want %v, ok %v", tc.name, got,
				log.String(), tc.want, tc.ok)
		}
	}
}
