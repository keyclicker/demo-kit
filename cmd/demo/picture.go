package main

import (
	"cmp"
	"fmt"
	"image"
	"math"
)

// Images are drawn like everything else: fitted onto a canvas here,
// so a plain image and a spotlit one are the same picture, and steps
// can come and go without changing how the scene renders.

// ---- Image ----

// picture is an image fitted to a canvas: the frame, or the frame's
// width and as tall as it needs (then paged). Source pixel p lands at
// at + p*scale.
type picture struct {
	canvas *image.RGBA
	src    image.Rectangle
	scale  float64
	at     image.Point
}

// loadPicture decodes path and fits it (fitPicture).
func loadPicture(path string) (*picture, error) {
	src, err := loadImage(path)
	if err != nil {
		return nil, err
	}
	return fitPicture(path, src)
}

// fitPicture fits src, from path: whole into the frame, or to its
// width if that leaves it taller than the frame.
func fitPicture(path string, src image.Image) (*picture, error) {
	b := src.Bounds()
	tall := int(math.Round(float64(b.Dy()) * frameW / float64(b.Dx())))
	if tall > maxDrawH {
		return nil, fmt.Errorf("%s: %d px tall at the frame's width, over "+
			"%d: crop it", path, tall, maxDrawH)
	}

	canvas := newCanvas(tall)
	scale, at := fitInto(canvas, src, canvas.Bounds())
	return &picture{canvas: canvas, src: b, scale: scale, at: at}, nil
}

// aim is a step aimed at a picture, in canvas pixels: its spotlit
// box (empty for none), the view top that centers its zoom or box, and
// the zoom's view (zoomView; the whole frame for none).
type aim struct {
	box  image.Rectangle
	y    int // -1: the last step's
	view rect
}

// aim places a step's box and zoom (source pixels) on the canvas.
// Only numbers: steps are aimed up front, drawn lazily (point).
func (p *picture) aim(st Step) (aim, error) {
	a := aim{y: -1, view: fullFrame}
	var zoom image.Rectangle
	var err error

	if st.Box != nil {
		if a.box, err = p.region("box", st.Box.Rect); err != nil {
			return a, err
		}
	}
	if st.Zoom != nil {
		if zoom, err = p.region("zoom", st.Zoom.Rect); err != nil {
			return a, err
		}
	}

	// The view centers on what the step looks at: its zoom, else its box
	target := cmp.Or(zoom, a.box)
	if target.Empty() {
		return a, nil
	}
	a.y = centerY(target.Min.Y, target.Max.Y, p.canvas.Bounds().Dy())

	if !zoom.Empty() {
		a.view = zoomView(rect{float64(zoom.Min.X),
			float64(zoom.Min.Y - a.y), float64(zoom.Dx()),
			float64(zoom.Dy())})
	}
	return a, nil
}

// region maps a step's [x, y, w, h] (source pixels) onto the canvas.
func (p *picture) region(name string, r []int) (image.Rectangle, error) {
	if err := p.inside(r[0]+r[2], r[1]+r[3]); err != nil {
		return image.Rectangle{}, fmt.Errorf("%s %v: %w", name, r, err)
	}
	return image.Rectangle{p.place(r[0], r[1]),
		p.place(r[0]+r[2], r[1]+r[3])}, nil
}

// point is the picture as an aimed step shows it: with a box, spotlit
// (everything else fades as unfocused code does, the box outlined, as
// thick on screen however close the zoom); without, plain.
func (p *picture) point(a aim) stepView {
	if a.box.Empty() {
		return stepView{p.canvas, nil, a.y}
	}

	img := image.NewRGBA(p.canvas.Bounds())
	copy(img.Pix, p.canvas.Pix)
	t := max(1, int(math.Round(focusBar/2*a.view.w/frameW)))
	spotlight(img, a.box, t)
	return stepView{img, nil, a.y}
}

// inside checks source point (x, y) is on the image.
func (p *picture) inside(x, y int) error {
	if x > p.src.Dx() || y > p.src.Dy() {
		return fmt.Errorf("outside the image (%dx%d)", p.src.Dx(),
			p.src.Dy())
	}
	return nil
}

// place maps source pixel (x, y) to the canvas.
func (p *picture) place(x, y int) image.Point {
	return image.Pt(p.at.X+int(math.Round(float64(x)*p.scale)),
		p.at.Y+int(math.Round(float64(y)*p.scale)))
}

// spotlight fades img around r and outlines r, t px thick: just
// outside it, or inside where r meets the edge, so the outline is
// always whole.
func spotlight(img *image.RGBA, r image.Rectangle, t int) {
	b := img.Bounds()
	for _, around := range []image.Rectangle{
		image.Rect(0, 0, b.Dx(), r.Min.Y),
		image.Rect(0, r.Max.Y, b.Dx(), b.Dy()),
		image.Rect(0, r.Min.Y, r.Min.X, r.Max.Y),
		image.Rect(r.Max.X, r.Min.Y, b.Dx(), r.Max.Y),
	} {
		fade(img, around)
	}

	o := r.Inset(-t).Intersect(b)
	fill(img, o.Min.X, o.Min.Y, o.Dx(), t, colFocus)
	fill(img, o.Min.X, o.Max.Y-t, o.Dx(), t, colFocus)
	fill(img, o.Min.X, o.Min.Y, t, o.Dy(), colFocus)
	fill(img, o.Max.X-t, o.Min.Y, t, o.Dy(), colFocus)
}
