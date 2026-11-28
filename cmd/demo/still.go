package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
)

// A clip's still edges go before timing: the cut starts just before
// its first change and ends just after its last, so a still lead or
// tail never plays (nor stretches a silent clip). With its sound kept,
// an edge goes only where it's quiet too. After the build, the video
// itself is checked for dead air: still and silent at once.
//
// Motion is mpdecimate's, as in clipScene: a frame changes when an
// 8x8 block of it does, so a moving pointer counts and encoder noise
// doesn't.

const (
	stillHold  = 0.5 // of a still edge, this much stays: the result shows
	deadAirMin = 1.5 // still and silent this long: warn
)

var ptsRE = regexp.MustCompile(`pts_time:\s*(-?[0-9.]+)`)

// changes is when a video's picture changes, in seconds from its
// start: the frames mpdecimate keeps, first frame included. cut limits
// it to a stretch (zero: the whole file); times stay the file's.
func changes(ctx context.Context, video string, cut span) ([]float64,
	error) {

	var args []string
	if cut.from > 0 {
		args = append(args, "-ss", ftoa(cut.from))
	}
	if cut.to > 0 {
		args = append(args, "-to", ftoa(cut.to))
	}
	args = append(args, "-i", video, "-map", "0:v:0", "-vf",
		"mpdecimate,showinfo", "-f", "null", "-")

	out, err := exec.CommandContext(ctx, "ffmpeg", append([]string{
		"-hide_banner", "-nostats", "-nostdin"}, args...)...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("motion in %s: %v: %s", video, err, out)
	}
	return parseChanges(string(out), cut.from), nil
}

// parseChanges reads showinfo's log: frame times, shifted by from (an
// input seek restarts them at 0).
func parseChanges(log string, from float64) []float64 {
	var ts []float64
	for _, m := range ptsRE.FindAllStringSubmatch(log, -1) {
		t, _ := strconv.ParseFloat(m[1], 64)
		ts = append(ts, from+max(t, 0))
	}
	return ts
}

// ---- Still edges ----

// trimStill cuts c's still edges: kept is when its picture changes
// (changes: the first is its first frame), quiet its pauses (all of c
// for a clip played without sound). The cut starts stillHold before
// the first thing that happens, a change or a sound, and ends stillHold
// after the last. ok is false when nothing happens: c is kept then.
func trimStill(c span, kept []float64, quiet []span) (out span, ok bool) {
	first, last := c.to, c.from
	if len(kept) > 1 {
		first, last = kept[1], kept[len(kept)-1]
	}

	// Sound at an edge (not in a pause) holds that edge where it is
	if p, in := pauseAt(quiet, c.from); !in {
		first = c.from
	} else if p.to < c.to {
		first = min(first, p.to)
	}
	if p, in := pauseAt(quiet, c.to); !in {
		last = c.to
	} else if p.from > c.from {
		last = max(last, p.from)
	}

	if first > last {
		return c, false
	}
	return span{max(c.from, first-stillHold), min(c.to, last+stillHold)},
		true
}

// pauseAt is the pause of ps that t is in.
func pauseAt(ps []span, t float64) (span, bool) {
	for _, p := range ps {
		if p.from <= t && t <= p.to {
			return p, true
		}
	}
	return span{}, false
}

// trimCut is scene s's cut c without its still edges (trimStill),
// saying what moved on stderr.
func trimCut(ctx context.Context, w io.Writer, s Scene, c span) (span,
	error) {

	kept, err := changes(ctx, s.Clip, c)
	if err != nil {
		return c, err
	}

	quiet := []span{c}
	if s.Audio {
		snd, err := listen(ctx, s.Clip)
		if err != nil {
			return c, err
		}
		quiet = snd.pauses
	}

	t, ok := trimStill(c, kept, quiet)
	switch {
	case !ok:
		fmt.Fprintf(w, "demo: scene %d: nothing moves or sounds in "+
			"%.1f-%.1f s: cut it elsewhere\n", s.num, c.from, c.to)
	case t.from-c.from >= 0.1 || c.to-t.to >= 0.1: // too little: unsaid
		fmt.Fprintf(w, "demo: scene %d: still edges trimmed: %.1f-%.1f s "+
			"→ %.1f-%.1f s (trim: false keeps them)\n", s.num, c.from, c.to,
			t.from, t.to)
	}
	return t, nil
}

// ---- Dead air ----

// stills are the stretches between a video's changes (changes), the
// last running to its end.
func stills(kept []float64, end float64) []span {
	var ss []span
	for i, t := range kept {
		next := end
		if i+1 < len(kept) {
			next = kept[i+1]
		}
		ss = append(ss, span{t, next})
	}
	return ss
}

// deadAir is where stills and quiet overlap for deadAirMin or longer.
func deadAir(stills, quiet []span) []span {
	var dead []span
	for _, s := range stills {
		for _, q := range quiet {
			d := span{max(s.from, q.from), min(s.to, q.to)}
			if d.to-d.from >= deadAirMin {
				dead = append(dead, d)
			}
		}
	}
	return dead
}

// warnDeadAir checks the built video for dead air, naming each
// stretch's scene.
func (b *builder) warnDeadAir(ctx context.Context, w io.Writer,
	video string) error {

	snd, err := listen(ctx, video)
	if err != nil {
		return err
	}
	kept, err := changes(ctx, video, span{})
	if err != nil {
		return err
	}

	for _, d := range deadAir(stills(kept, snd.length), snd.pauses) {
		fmt.Fprintf(w, "demo: dead air %s-%s (%.1f s, scene %d): nothing "+
			"moves or speaks: say more, cut the clip, or drop secs\n",
			clockTenths(d.from), clockTenths(d.to), d.to-d.from,
			b.sceneAt(d.from).num)
	}
	return nil
}

// sceneAt is the scene playing at t in the video. Counted in whole
// frames, as scenes are long (sceneTiming): a float sum of lengths
// against showinfo's rounded times blames a scene's first frame on
// the one before.
func (b *builder) sceneAt(t float64) Scene {
	frame, at, i := frameCount(t), 0, 0
	for i < len(b.timings)-1 && at+frameCount(b.timings[i].Length) <= frame {
		at += frameCount(b.timings[i].Length)
		i++
	}
	return b.script.Scenes[i]
}

// clockTenths is secs as m:ss.s.
func clockTenths(secs float64) string {
	tenths := int(secs*10 + 0.5)
	return fmt.Sprintf("%d:%02d.%d", tenths/600, tenths/10%60, tenths%10)
}
