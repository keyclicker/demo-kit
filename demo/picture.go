package main

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/vector"
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

// loadPicture decodes path and fits it: whole into the frame, or to
// its width if that leaves it taller than the frame.
func loadPicture(path string) (*picture, error) {
	src, err := loadImage(path)
	if err != nil {
		return nil, err
	}

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

// point is the picture with a step's pointers on it, in source
// pixels: Box [x, y, w, h] is spotlit (everything else fades as
// unfocused code does, the box outlined), Arrow [x, y] gets an arrow's
// tip. The view centers on them.
func (p *picture) point(st Step) (stepView, error) {
	img := image.NewRGBA(p.canvas.Bounds())
	copy(img.Pix, p.canvas.Pix)
	var spans []image.Rectangle

	if st.Box != nil {
		b := st.Box
		if err := p.inside(b[0]+b[2], b[1]+b[3]); err != nil {
			return stepView{}, fmt.Errorf("box %v: %w", b, err)
		}
		r := image.Rectangle{p.place(b[0], b[1]),
			p.place(b[0]+b[2], b[1]+b[3])}
		spotlight(img, r)
		spans = append(spans, r)
	}

	if st.Arrow != nil {
		a := st.Arrow
		if err := p.inside(a[0], a[1]); err != nil {
			return stepView{}, fmt.Errorf("arrow %v: %w", a, err)
		}
		spans = append(spans, drawArrow(img, p.place(a[0], a[1])))
	}

	all := spans[0]
	for _, r := range spans[1:] {
		all = all.Union(r)
	}
	return stepView{img, centerY(all.Min.Y, all.Max.Y, img.Bounds().Dy())},
		nil
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

// spotlight fades img around r and outlines r: just outside it, or
// inside where r meets the edge, so the outline is always whole.
func spotlight(img *image.RGBA, r image.Rectangle) {
	b := img.Bounds()
	for _, around := range []image.Rectangle{
		image.Rect(0, 0, b.Dx(), r.Min.Y),
		image.Rect(0, r.Max.Y, b.Dx(), b.Dy()),
		image.Rect(0, r.Min.Y, r.Min.X, r.Max.Y),
		image.Rect(r.Max.X, r.Min.Y, b.Dx(), r.Max.Y),
	} {
		fade(img, around)
	}

	const t = focusBar / 2
	o := r.Inset(-t).Intersect(b)
	fill(img, o.Min.X, o.Min.Y, o.Dx(), t, colFocus)
	fill(img, o.Min.X, o.Max.Y-t, o.Dx(), t, colFocus)
	fill(img, o.Min.X, o.Min.Y, t, o.Dy(), colFocus)
	fill(img, o.Max.X-t, o.Min.Y, t, o.Dy(), colFocus)
}

// The arrow, px: it comes in at 45°, from the side with more room
const (
	arrowLen   = 150
	arrowShaft = 5  // half its width
	arrowHead  = 20 // half the head's width
	arrowHeadL = 40 // the head's length
	arrowShade = 3  // a dark copy this far down-right, for light images
	arrowGap   = 6  // between the tip and the spot, so the spot shows
)

// drawArrow draws an arrow pointing at spot, coming from the picture's
// horizontal middle and from below (above near the bottom), and
// returns the box it covers.
func drawArrow(img *image.RGBA, spot image.Point) image.Rectangle {
	b := img.Bounds()
	dx, dy := 1.0, 1.0 // tip to tail
	if spot.X > b.Dx()/2 {
		dx = -1
	}
	if spot.Y+arrowLen+margin > b.Dy() {
		dy = -1
	}

	// Along the arrow (tail to tip) and across it, unit vectors
	ux, uy := -dx/math.Sqrt2, -dy/math.Sqrt2
	nx, ny := -uy, ux

	tx := float64(spot.X) - ux*arrowGap
	ty := float64(spot.Y) - uy*arrowGap
	tailX, tailY := tx-ux*arrowLen, ty-uy*arrowLen
	baseX, baseY := tx-ux*arrowHeadL, ty-uy*arrowHeadL

	pt := func(x, y, across float64) [2]float64 {
		return [2]float64{x + nx*across, y + ny*across}
	}
	shape := [][2]float64{
		pt(tailX, tailY, arrowShaft), pt(baseX, baseY, arrowShaft),
		pt(baseX, baseY, arrowHead), {tx, ty},
		pt(baseX, baseY, -arrowHead), pt(baseX, baseY, -arrowShaft),
		pt(tailX, tailY, -arrowShaft),
	}

	fillPolygon(img, shape, arrowShade, color.NRGBA{0, 0, 0, 0xa0})
	return fillPolygon(img, shape, 0, colFocus)
}

// fillPolygon fills shape, moved by (off, off), anti-aliased, and
// returns the box it covers.
func fillPolygon(img *image.RGBA, shape [][2]float64, off float64,
	c color.Color) image.Rectangle {

	var box image.Rectangle
	for k, q := range shape {
		x, y := int(q[0]+off), int(q[1]+off)
		r := image.Rect(x-1, y-1, x+2, y+2)
		if k == 0 {
			box = r
		}
		box = box.Union(r)
	}

	// A mask the shape's size, not the picture's (a tall picture would
	// cost a float per pixel), drawn clipped: the rasterizer doesn't clip
	z := vector.NewRasterizer(box.Dx(), box.Dy())
	for k, q := range shape {
		x := float32(q[0] + off - float64(box.Min.X))
		y := float32(q[1] + off - float64(box.Min.Y))
		if k == 0 {
			z.MoveTo(x, y)
		} else {
			z.LineTo(x, y)
		}
	}
	z.ClosePath()

	mask := image.NewAlpha(image.Rect(0, 0, box.Dx(), box.Dy()))
	z.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	draw.DrawMask(img, box, image.NewUniform(c), image.Point{}, mask,
		image.Point{}, draw.Over)
	return box.Intersect(img.Bounds())
}

// ---- Compare ----

// Pane is one image of a compare scene, with a label over it.
type Pane struct {
	Image string `json:"image"`
	Label string `json:"label,omitempty"`
}

// checkCompare accepts 2-3 panes, each with an image.
func checkCompare(panes []Pane) error {
	if len(panes) < 2 || len(panes) > 3 {
		return errors.New("compare: 2 or 3 panes")
	}
	for i, p := range panes {
		if p.Image == "" {
			return fmt.Errorf("compare: pane %d needs an image", i+1)
		}
	}
	return nil
}

// drawCompare puts the panes' images side by side, each fitted under
// its label, with a seam between them.
func drawCompare(panes []Pane) (*image.RGBA, error) {
	const labelH, pad = 56, 16
	img := newCanvas(frameH)
	face := monoFace(true, 25)
	w := frameW / len(panes)

	for i, p := range panes {
		src, err := loadImage(p.Image)
		if err != nil {
			return nil, err
		}

		x := i * w
		if i > 0 {
			fill(img, x-1, 0, 2, frameH, colFileBG)
		}
		label := clip(p.Label, (w-2*pad)/textWidth(face, "M"))
		drawText(img, face, x+(w-textWidth(face, label))/2, margin, labelH,
			label, colText)

		fitInto(img, src, image.Rect(x+pad, margin+labelH, x+w-pad,
			frameH-margin))
	}
	return img, nil
}
