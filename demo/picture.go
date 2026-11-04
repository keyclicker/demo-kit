package main

import (
	"errors"
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

// spotlight is the picture with box [x, y, w, h] (source pixels) lit:
// everything else fades as unfocused code does, the box is outlined,
// and the view centers it.
func (p *picture) spotlight(box []int) (stepView, error) {
	w, h := p.src.Dx(), p.src.Dy()
	if box[0]+box[2] > w || box[1]+box[3] > h {
		return stepView{}, fmt.Errorf("box %v: outside the image (%dx%d)",
			box, w, h)
	}

	at := func(x, y int) image.Point {
		return image.Pt(p.at.X+int(math.Round(float64(x)*p.scale)),
			p.at.Y+int(math.Round(float64(y)*p.scale)))
	}
	r := image.Rectangle{at(box[0], box[1]),
		at(box[0]+box[2], box[1]+box[3])}

	img := image.NewRGBA(p.canvas.Bounds())
	copy(img.Pix, p.canvas.Pix)

	// Fade around the box: above, below, left, right
	bounds := img.Bounds()
	for _, around := range []image.Rectangle{
		image.Rect(0, 0, bounds.Dx(), r.Min.Y),
		image.Rect(0, r.Max.Y, bounds.Dx(), bounds.Dy()),
		image.Rect(0, r.Min.Y, r.Min.X, r.Max.Y),
		image.Rect(r.Max.X, r.Min.Y, bounds.Dx(), r.Max.Y),
	} {
		fade(img, around)
	}

	// An outline just outside it
	const t = focusBar / 2
	o := r.Inset(-t)
	fill(img, o.Min.X, o.Min.Y, o.Dx(), t, colFocus)
	fill(img, o.Min.X, r.Max.Y, o.Dx(), t, colFocus)
	fill(img, o.Min.X, r.Min.Y, t, r.Dy(), colFocus)
	fill(img, r.Max.X, r.Min.Y, t, r.Dy(), colFocus)

	return stepView{img, centerY(r.Min.Y, r.Max.Y, bounds.Dy())}, nil
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
