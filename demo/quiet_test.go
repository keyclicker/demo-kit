package main

import "testing"

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
}
