package main

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"
)

// Every scene but a clip is drawn here, in Go: code, images, slides,
// terminals. A drawn scene is a run of shots, each a picture and the
// frame-high view of it to show; they stream to ffmpeg as raw frames,
// so a cut lands on its exact frame and a tall picture costs no more
// than the view of it.

// ---- Frame ----

// The picture, before the caption band
const (
	frameW, frameH = 1280, 960

	// maxDrawH caps a drawing (~1000 lines of code): no scene shows
	// more, and beyond it memory runs away
	maxDrawH = 30 * frameH
)

// Dark palette, GitHub-like, to sit with chroma's github-dark
var (
	colBG       = rgb(0x0d1117)
	colFileBG   = rgb(0x161b22)
	colHunkBG   = rgb(0x111d2e)
	colAddBG    = rgb(0x12261e)
	colDelBG    = rgb(0x2d1215)
	colText     = rgb(0xe6edf3)
	colDim      = rgb(0x7d8590)
	colAdd      = rgb(0x3fb950)
	colDel      = rgb(0xf85149)
	colHunkText = rgb(0x79c0ff)
	colFocus    = rgb(0xf0b72f)

	// A mark: a translucent box behind the text, underlined
	colMark = color.NRGBA{0xf0, 0xb7, 0x2f, 0x40}

	// Out of focus fades toward the background. Overlays are NRGBA:
	// RGBA is premultiplied, and a component above alpha saturates
	colFade = color.NRGBA{0x0d, 0x11, 0x17, 0xb0}
)

func rgb(hex uint32) color.RGBA {
	return color.RGBA{uint8(hex >> 16), uint8(hex >> 8), uint8(hex), 0xff}
}

// newCanvas is a frameW-wide picture h tall (at least a frame), filled
// with the background.
func newCanvas(h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, frameW, max(h, frameH)))
	draw.Draw(img, img.Bounds(), image.NewUniform(colBG), image.Point{},
		draw.Src)
	return img
}

func fill(img *image.RGBA, x, y, w, h int, c color.Color) {
	draw.Draw(img, image.Rect(x, y, x+w, y+h), image.NewUniform(c),
		image.Point{}, draw.Src)
}

// fade dims r toward the background.
func fade(img *image.RGBA, r image.Rectangle) {
	draw.Draw(img, r, image.NewUniform(colFade), image.Point{}, draw.Over)
}

// ---- Text ----

// JetBrains Mono 2.304 (OFL, fonts/OFL.txt), embedded: no font to
// install, same look on every machine
var (
	//go:embed fonts/JetBrainsMono-Regular.ttf
	monoRegular []byte

	//go:embed fonts/JetBrainsMono-Bold.ttf
	monoBold []byte
)

// monoFonts parses both weights once; faces are made per drawing, as
// a face isn't safe for concurrent use.
var monoFonts = sync.OnceValue(func() [2]*opentype.Font {
	var fonts [2]*opentype.Font
	for i, ttf := range [][]byte{monoRegular, monoBold} {
		f, err := opentype.Parse(ttf)
		if err != nil {
			panic(fmt.Sprintf("embedded font: %v", err)) // a broken build
		}
		fonts[i] = f
	}
	return fonts
})

// monoFace is JetBrains Mono at size px. Its advance is 0.6 em: at
// sizes that are multiples of 5 a column is whole pixels.
func monoFace(bold bool, size float64) font.Face {
	face, err := opentype.NewFace(monoFonts()[btoi(bold)],
		&opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(fmt.Sprintf("font face: %v", err)) // only bad options fail
	}
	return face
}

// drawText draws s vertically centered in a row of height h at y, and
// returns the x after it.
func drawText(img *image.RGBA, face font.Face, x, y, h int, s string,
	c color.Color) int {

	m := face.Metrics()
	base := y + (h+m.Ascent.Round()-m.Descent.Round())/2

	d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: face,
		Dot: fixed.P(x, base)}
	d.DrawString(s)
	return d.Dot.X.Round()
}

// textWidth is s's width in face, px.
func textWidth(face font.Face, s string) int {
	return font.MeasureString(face, s).Round()
}

// clip cuts s to n runes, marking the cut.
func clip(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// maxGlyphsNamed caps how many missing characters a warning names.
const maxGlyphsNamed = 8

// warnGlyphs warns, on w, of the characters in texts JetBrains Mono
// has no glyph for (emoji, CJK): each draws as a box. where names the
// part, e.g. "scene 3". Control characters aside: they're never drawn.
func warnGlyphs(w io.Writer, where string, texts []string) {
	mono := monoFonts()[0]
	var buf sfnt.Buffer
	seen := map[rune]bool{}
	var missing []string

	for _, t := range texts {
		for _, r := range t {
			if seen[r] || unicode.IsControl(r) {
				continue
			}
			seen[r] = true

			// Glyph 0 is .notdef: the box
			if i, err := mono.GlyphIndex(&buf, r); err == nil && i == 0 {
				missing = append(missing, strconv.Quote(string(r)))
			}
		}
	}

	switch n := len(missing); {
	case n == 0:
		return
	case n > maxGlyphsNamed:
		missing = append(missing[:maxGlyphsNamed],
			fmt.Sprintf("%d more", n-maxGlyphsNamed))
	}
	what := "it draws as a box"
	if len(missing) > 1 {
		what = "they draw as boxes"
	}
	fmt.Fprintf(w, "demo: %s: no glyph for %s: %s\n", where,
		strings.Join(missing, ", "), what)
}

// ---- Images ----

// loadImage decodes a PNG, JPEG, GIF (its first frame) or WebP.
func loadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return img, nil
}

// fitInto scales src to fit r, centered, and draws it there; it
// returns where src's pixels landed: dst = at + src*scale.
func fitInto(dst *image.RGBA, src image.Image, r image.Rectangle) (
	scale float64, at image.Point) {

	b := src.Bounds()
	scale = min(float64(r.Dx())/float64(b.Dx()),
		float64(r.Dy())/float64(b.Dy()))
	w := int(math.Round(float64(b.Dx()) * scale))
	h := int(math.Round(float64(b.Dy()) * scale))

	at = image.Pt(r.Min.X+(r.Dx()-w)/2, r.Min.Y+(r.Dy()-h)/2)
	xdraw.CatmullRom.Scale(dst, image.Rect(at.X, at.Y, at.X+w, at.Y+h),
		src, b, draw.Src, nil)
	return scale, at
}

// savePNG writes img to path.
func savePNG(img image.Image, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	if err := png.Encode(w, img); err != nil {
		return err
	}
	return w.Flush()
}

// ---- Shots ----

// shot is a drawn picture held for n frames: the frame-high view of
// img at y. img is frameW wide and at least frameH tall.
type shot struct {
	img *image.RGBA
	y   int
	n   int
}

// shots produces a scene's shots in order, one at a time, so a scene
// holds one drawing at once however many steps it has.
type shots func(emit func(shot) error) error

// still is one picture for n frames: a view of its top if it fits, else
// its pages in turn.
func still(img *image.RGBA, n int) shots {
	return func(emit func(shot) error) error {
		tall := img.Bounds().Dy()
		pages := pageCount(tall)
		for k := range pages {
			y := min(k*pageStep, tall-frameH)
			start, end := n*k/pages, n*(k+1)/pages
			if err := emit(shot{img: img, y: y, n: end - start}); err != nil {
				return err
			}
		}
		return nil
	}
}

// pageCount is how many frames, pageStep apart, show a picture of
// height tall: the last one flush with the bottom.
func pageCount(tall int) int {
	return 1 + max(0, tall-frameH+pageStep-1)/pageStep
}

// stepView is one step's picture and the top of its view; y < 0 keeps
// the last step's view (the top, first).
type stepView struct {
	img *image.RGBA
	y   int
}

// stepShots is one shot per step, cut at the scene's step cuts (in
// frames), each from view(k).
func stepShots(cuts []int, n int,
	view func(k int) (stepView, error)) shots {

	return func(emit func(shot) error) error {
		y := 0
		for k := range cuts {
			v, err := view(k)
			if err != nil {
				return fmt.Errorf("step %d: %w", k+1, err)
			}
			if v.y >= 0 {
				y = v.y
			}

			end := n
			if k+1 < len(cuts) {
				end = cuts[k+1]
			}
			if err := emit(shot{img: v.img, y: y, n: end - cuts[k]}); err != nil {
				return err
			}
		}
		return nil
	}
}

// centerY is the view top that centers [top, bottom) in a frame of a
// picture tall px high; a span taller than the frame shows its top.
func centerY(top, bottom, tall int) int {
	y := (top+bottom)/2 - frameH/2
	if bottom-top > frameH-2*margin {
		y = top - margin
	}
	return min(max(y, 0), tall-frameH)
}

// drawnVideo streams shots to ffmpeg as exactly n raw frames, through
// filter vf: the last shot is held or cut to fit.
func (b *builder) drawnVideo(ctx context.Context, n int, shots shots,
	vf, out string) error {

	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := ffmpeg(ctx, "", r, append([]string{
			"-f", "rawvideo", "-pix_fmt", "rgba",
			"-s", fmt.Sprintf("%dx%d", frameW, frameH),
			"-framerate", fmt.Sprint(fps), "-i", "pipe:0",
			"-vf", vf}, append(b.codec, out)...)...)
		r.CloseWithError(firstErr(err, io.ErrClosedPipe)) // unblock the writer
		done <- err
	}()

	written := 0
	var last []byte
	err := shots(func(s shot) error {
		for range s.n {
			if written == n {
				return nil
			}
			last = view(s)
			if _, err := w.Write(last); err != nil {
				return err
			}
			written++
		}
		return nil
	})

	for ; err == nil && written < n; written++ {
		if last == nil {
			err = errors.New("nothing drawn")
			break
		}
		_, err = w.Write(last)
	}

	// A drawing's error explains ffmpeg's, which only saw its input end
	w.CloseWithError(err)
	if ferr := <-done; err == nil {
		err = ferr
	}
	return err
}

// view is s's frame: its view's rows, straight out of the picture (a
// frameW-wide RGBA's rows are contiguous).
func view(s shot) []byte {
	stride := s.img.Stride
	return s.img.Pix[s.y*stride : (s.y+frameH)*stride]
}

// firstErr is the first of its arguments that isn't nil.
func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
