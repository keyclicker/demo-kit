package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// A clip that keeps its sound (a narrated demo, someone talking) is
// cut in its pauses, never mid-word: from and to move into the nearest
// one, and a file that itself ends mid-speech is cut back to its last.

const (
	quietDB     = -35.0 // below this, the clip's sound is a pause
	quietMin    = 0.25  // a pause is at least this long: between phrases
	quietWindow = 3.0   // a cut moves this far, at most, to land in one
	quietPad    = 0.25  // this far into the pause it lands, room allowing
)

// span is a stretch of a clip, in seconds.
type span struct{ from, to float64 }

// quietCuts is where a sound-keeping clip's cut really goes: s.From and
// s.To (0: the file's end) moved into pauses, saying so on stderr.
func quietCuts(ctx context.Context, s Scene) (from, to float64,
	err error) {

	sound, err := hasAudio(ctx, s.Clip)
	if err != nil {
		return 0, 0, err
	}
	if !sound {
		return 0, 0, fmt.Errorf("%s has no sound to keep: drop audio",
			s.Clip)
	}

	snd, err := listen(ctx, s.Clip)
	if err != nil {
		return 0, 0, err
	}

	move := func(name string, t float64, end bool) float64 {
		at, ok := cutAt(snd.pauses, t, end)
		switch {
		case !ok:
			fmt.Fprintf(os.Stderr, "demo: scene %d: %s %.1f s is mid-speech,"+
				" no pause within %g s: cut it elsewhere\n", s.num, name, t,
				quietWindow)
		case at != t:
			fmt.Fprintf(os.Stderr, "demo: scene %d: %s %.1f → %.1f s, "+
				"into a pause in its sound\n", s.num, name, t, at)
		}
		return at
	}

	from, to = s.From, s.To
	if from > 0 {
		from = move("from", from, false)
	}
	switch {
	case to > 0:
		to = move("to", to, true)
	case snd.loud:
		// No to: the file's own end, mid-speech if it was cut so
		to = move("its end", snd.end, true)
	}

	// Moved into pauses, a start can pass its end
	if to > 0 && from >= to {
		fmt.Fprintf(os.Stderr, "demo: scene %d: the moved cuts cross "+
			"(from %.1f s, to %.1f s): kept the script's own\n", s.num,
			from, to)
		return s.From, s.To, nil
	}
	return from, to, nil
}

// cutAt moves a cut at t into a pause of ps: an end finishes its phrase
// (the next pause) or else stops before it (the last one); a start
// begins its phrase, or else the next. ok is false when no pause is
// within quietWindow; t is kept then.
func cutAt(ps []span, t float64, end bool) (at float64, ok bool) {
	var prev, next *span
	for i := range ps {
		p := &ps[i]
		switch {
		case p.from <= t && t <= p.to:
			return t, true
		case p.to < t:
			prev = p
		case next == nil:
			next = p
		}
	}

	pad := func(p *span) float64 { return min(quietPad, (p.to-p.from)/2) }
	hasPrev := prev != nil && t-prev.to <= quietWindow
	hasNext := next != nil && next.from-t <= quietWindow

	switch {
	case end && hasNext:
		return next.from + pad(next), true
	case end && hasPrev:
		return prev.from + pad(prev), true
	case !end && hasPrev:
		return prev.to - pad(prev), true
	case !end && hasNext:
		return next.to - pad(next), true
	}
	return t, false
}

// sound is what a clip's audio does, as cuts need it.
type sound struct {
	length float64 // the file's
	end    float64 // the audio stream's own: it may stop before the video
	loud   bool    // loud in its last 0.1 s: cut mid-speech

	// pauses are its quiet spans, quietMin or longer. Past end is quiet
	// too, unless it ends loud: speech cut off is no pause.
	pauses []span
}

// listen probes clip's sound (its first audio stream).
func listen(ctx context.Context, clip string) (sound, error) {
	length, err := probeDuration(ctx, clip)
	if err != nil {
		return sound{}, err
	}

	end, err := audioEnd(ctx, clip, length)
	if err != nil {
		return sound{}, err
	}

	loud, err := endsLoud(ctx, clip, end)
	if err != nil {
		return sound{}, err
	}

	ps, err := pauses(ctx, clip, end)
	if err != nil {
		return sound{}, err
	}
	if !loud {
		ps = quietTail(ps, end, length)
	}

	return sound{length: length, end: end, loud: loud, pauses: ps}, nil
}

// audioEnd is where clip's first audio stream ends: a recording's
// sound often stops a few frames before its picture. length when the
// container doesn't say.
func audioEnd(ctx context.Context, clip string, length float64) (float64,
	error) {

	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-select_streams", "a:0", "-show_entries",
		"stream=start_time,duration", "-of", "csv=p=0", clip).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe %s: %w", clip, err)
	}

	start, dur, _ := strings.Cut(strings.TrimSpace(string(out)), ",")
	from, err1 := strconv.ParseFloat(start, 64)
	secs, err2 := strconv.ParseFloat(dur, 64)
	if err1 != nil || err2 != nil {
		return length, nil
	}
	return min(from+secs, length), nil
}

// quietTail adds the stretch from end (the audio's) to length (the
// file's) to ps, joined to a pause that reaches end.
func quietTail(ps []span, end, length float64) []span {
	if end >= length {
		return ps
	}

	if n := len(ps); n > 0 && ps[n-1].to >= end {
		ps[n-1].to = length
		return ps
	}
	return append(ps, span{end, length})
}

var silenceRE = regexp.MustCompile(`silence_(start|end): (-?[0-9.]+)`)

// pauses are a clip's quiet spans, quietMin or longer (silencedetect);
// one still open at the audio's end runs to end.
func pauses(ctx context.Context, clip string, end float64) ([]span,
	error) {

	out, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner",
		"-nostats", "-nostdin", "-i", clip, "-map", "0:a:0", "-af",
		fmt.Sprintf("silencedetect=n=%gdB:d=%g", quietDB, quietMin),
		"-f", "null", "-").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("pauses in %s: %v: %s", clip, err, out)
	}
	return parsePauses(string(out), end), nil
}

// parsePauses reads silencedetect's log; a pause still open runs to end.
func parsePauses(log string, end float64) []span {
	var ps []span
	open := -1.0
	for _, m := range silenceRE.FindAllStringSubmatch(log, -1) {
		t, _ := strconv.ParseFloat(m[2], 64)
		if m[1] == "start" {
			open = max(t, 0)
		} else if open >= 0 {
			ps = append(ps, span{open, t})
			open = -1
		}
	}
	if open >= 0 {
		ps = append(ps, span{open, end})
	}
	return ps
}

var meanRE = regexp.MustCompile(`mean_volume: (-?[0-9.]+) dB`)

// endsLoud reports whether a clip's sound is loud in its last 0.1 s,
// up to end (the audio's own): cut mid-speech. (A speech file's short
// quiet tail is too short for pauses to see.) Not -sseof: that counts
// from the container's end, past a shorter audio stream.
func endsLoud(ctx context.Context, clip string, end float64) (bool,
	error) {

	out, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner",
		"-nostats", "-nostdin", "-ss", ftoa(max(0, end-0.1)), "-i", clip,
		"-map", "0:a:0", "-af", "volumedetect", "-f", "null",
		"-").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("loudness of %s: %v: %s", clip, err, out)
	}

	m := meanRE.FindStringSubmatch(string(out))
	if m == nil {
		return false, nil // silent to the end: no volume to report
	}
	db, _ := strconv.ParseFloat(m[1], 64)
	return db > quietDB, nil
}
