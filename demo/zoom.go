package main

import (
	"context"
	"fmt"
	"slices"
)

// Zoom points a recording at what its narration talks about: a step's
// zoom [x, y, w, h] (the clip's pixels) fills the frame while it's
// said, eased in at the step's cut; a step without one shows it all.
//
// ffmpeg's perspective filter does it, sub-pixel, with corners that
// move per frame. Not crop: it sizes itself to the first frame, so it
// can't follow a scale that changes per frame. Not zoompan: it crops at
// whole source pixels, so its zooms jitter.

const (
	zoomSecs = 0.5 // a zoom eases this long
	maxZoom  = 4.0 // past it a recording is mush: smaller regions grow
)

// rect is a region of the frame, in frame pixels.
type rect struct{ x, y, w, h float64 }

// fullFrame is the whole frame: no zoom.
var fullFrame = rect{0, 0, frameW, frameH}

// clipViews is what each of a clip's steps shows (zoomRect), nil when
// no step zooms.
func clipViews(ctx context.Context, s Scene) ([]rect, error) {
	if !slices.ContainsFunc(s.Steps, func(st Step) bool {
		return st.Zoom != nil
	}) {
		return nil, nil
	}

	w, h, err := probeSize(ctx, s.Clip)
	if err != nil {
		return nil, err
	}

	var views []rect
	for k, st := range s.Steps {
		v, err := zoomRect(st.Zoom, w, h)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", k+1, err)
		}
		views = append(views, v)
	}
	return views, nil
}

// zoomRect is the frame region a step's zoom shows: the clip's region
// (src pixels, srcW x srcH) mapped through the fit into the frame,
// grown to the frame's shape around its center, at most maxZoom in,
// and kept inside the frame. No zoom: the whole frame.
func zoomRect(zoom []int, srcW, srcH int) (rect, error) {
	if zoom == nil {
		return fullFrame, nil
	}
	if zoom[0]+zoom[2] > srcW || zoom[1]+zoom[3] > srcH {
		return rect{}, fmt.Errorf("zoom %v: outside the clip (%dx%d)", zoom,
			srcW, srcH)
	}

	// The fit: scaled into the frame, centered (fitFilter)
	s := min(frameW/float64(srcW), frameH/float64(srcH))
	ox := (frameW - float64(srcW)*s) / 2
	oy := (frameH - float64(srcH)*s) / 2
	r := rect{ox + float64(zoom[0])*s, oy + float64(zoom[1])*s,
		float64(zoom[2]) * s, float64(zoom[3]) * s}

	// The frame's shape, never closer than maxZoom
	const aspect = float64(frameW) / frameH
	w := min(max(r.w, r.h*aspect, frameW/maxZoom), frameW)
	h := w / aspect

	cx, cy := r.x+r.w/2, r.y+r.h/2
	return rect{
		x: min(max(cx-w/2, 0), frameW-w),
		y: min(max(cy-h/2, 0), frameH-h),
		w: w, h: h,
	}, nil
}

// zoomFilter is ffmpeg's perspective filter that shows views[k] from
// cuts[k] on, eased in from the one before (smoothstep) over zoomSecs,
// or the step's length if shorter; n is the scene's frames.
//
// Each edge is one per-frame expression: the first view's edge plus
// every later step's change, eased. In fractions of W and H, so a
// draft's half-size frame zooms alike.
func zoomFilter(views []rect, cuts []int, n int) string {
	edge := func(size string, full float64, at func(rect) float64) string {
		// ld(1): the edge so far, ld(0): a step's ease; in counts from 1
		e := fmt.Sprintf("st(1,%g)", at(views[0])/full)
		for k := 1; k < len(views); k++ {
			d := (at(views[k]) - at(views[k-1])) / full
			if d == 0 {
				continue
			}

			end := n
			if k+1 < len(cuts) {
				end = cuts[k+1]
			}
			m := max(1, min(frameCount(zoomSecs), end-cuts[k]))

			e += fmt.Sprintf(";st(0,clip((in-1-%d)/%d,0,1))"+
				";st(1,ld(1)%+g*ld(0)*ld(0)*(3-2*ld(0)))", cuts[k], m, d)
		}
		return fmt.Sprintf("%s;%s*ld(1)", e, size)
	}

	left := edge("W", frameW, func(v rect) float64 { return v.x })
	right := edge("W", frameW, func(v rect) float64 { return v.x + v.w })
	top := edge("H", frameH, func(v rect) float64 { return v.y })
	bottom := edge("H", frameH, func(v rect) float64 { return v.y + v.h })

	// The source's corners that land on the frame's: TL, TR, BL, BR
	return fmt.Sprintf("perspective=x0='%[1]s':y0='%[3]s':x1='%[2]s':"+
		"y1='%[3]s':x2='%[1]s':y2='%[4]s':x3='%[2]s':y3='%[4]s':"+
		"interpolation=cubic:eval=frame", left, right, top, bottom)
}
