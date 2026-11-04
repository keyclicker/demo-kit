package main

import (
	"bufio"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// ---- Parsed diff ----

// diffFile is one file of a unified diff.
type diffFile struct {
	Path       string
	Status     string // "", "new", "deleted", "renamed"
	Binary     bool
	Hunks      []*hunk
	Adds, Dels int
}

// hunk is one @@ block. Index counts hunks across the whole diff,
// from 1: it's how scenes and `demo diff -hunks` pick them.
type hunk struct {
	Index              int
	OldStart, NewStart int
	Context            string // git's function context after the @@
	Lines              []diffLine
	Adds, Dels         int
}

// diffLine is one line of a hunk: Kind ' ', '+' or '-'.
type diffLine struct {
	Kind     byte
	Old, New int // line numbers, 0 where the side has none
	Text     string
}

var hunkRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$`)

// parseDiff reads `git diff` output. Hunk bodies are consumed by their
// line counts, so content that looks like a header stays content.
func parseDiff(text string) ([]*diffFile, error) {
	var files []*diffFile
	var file *diffFile
	var h *hunk
	var oldLeft, newLeft, oldN, newN, index int

	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		// ---- Hunk body ----

		if h != nil && (oldLeft > 0 || newLeft > 0) {
			if strings.HasPrefix(line, `\`) {
				continue // "\ No newline at end of file"
			}

			kind := byte(' ')
			if line != "" {
				kind = line[0]
			}
			dl := diffLine{Kind: kind}
			if len(line) > 0 {
				dl.Text = line[1:]
			}

			switch kind {
			case ' ':
				dl.Old, dl.New = oldN, newN
				oldN, newN, oldLeft, newLeft = oldN+1, newN+1, oldLeft-1, newLeft-1
			case '-':
				dl.Old = oldN
				oldN, oldLeft = oldN+1, oldLeft-1
				h.Dels++
				file.Dels++
			case '+':
				dl.New = newN
				newN, newLeft = newN+1, newLeft-1
				h.Adds++
				file.Adds++
			default:
				return nil, fmt.Errorf("hunk #%d: bad line %q", h.Index, line)
			}

			h.Lines = append(h.Lines, dl)
			continue
		}

		// ---- Headers ----

		switch {
		case strings.HasPrefix(line, "diff --git "):
			file = &diffFile{Path: gitPath(line)}
			files = append(files, file)
			h = nil

		case file == nil:
			continue

		case strings.HasPrefix(line, "new file mode"):
			file.Status = "new"
		case strings.HasPrefix(line, "deleted file mode"):
			file.Status = "deleted"
		case strings.HasPrefix(line, "rename to "):
			file.Status = "renamed"
			file.Path = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "Binary files "):
			file.Binary = true

		case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "):
			if p := strings.TrimPrefix(line[4:], "b/"); line[0] == '+' &&
				p != "/dev/null" {
				file.Path = p
			}

		case strings.HasPrefix(line, "@@"):
			m := hunkRE.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("bad hunk header %q", line)
			}

			index++
			oldN, _ = strconv.Atoi(m[1])
			newN, _ = strconv.Atoi(m[3])
			oldLeft, newLeft = hunkCount(m[2]), hunkCount(m[4])

			h = &hunk{Index: index, OldStart: oldN, NewStart: newN,
				Context: strings.TrimSpace(m[5])}
			file.Hunks = append(file.Hunks, h)
		}
	}

	return files, nil
}

// hunkCount parses a hunk header's optional line count (default 1).
func hunkCount(s string) int {
	if s == "" {
		return 1
	}
	n, _ := strconv.Atoi(s)
	return n
}

// gitPath takes the b/ side of "diff --git a/X b/X".
func gitPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return rest
}

// pickHunks keeps only the listed hunks (all when empty), and drops
// files left with none.
func pickHunks(files []*diffFile, picks []int) []*diffFile {
	if len(picks) == 0 {
		return files
	}

	want := map[int]bool{}
	for _, i := range picks {
		want[i] = true
	}

	var out []*diffFile
	for _, f := range files {
		kept := *f
		kept.Hunks, kept.Adds, kept.Dels = nil, 0, 0

		for _, h := range f.Hunks {
			if want[h.Index] {
				kept.Hunks = append(kept.Hunks, h)
				kept.Adds += h.Adds
				kept.Dels += h.Dels
			}
		}
		if len(kept.Hunks) > 0 {
			out = append(out, &kept)
		}
	}
	return out
}

// cropLines keeps only lines whose new-side number is in [from, to],
// plus removed lines sitting among them: a new file is one hunk, and
// this cuts the part worth showing. Hunks left empty go, then files.
func cropLines(files []*diffFile, from, to int) []*diffFile {
	var out []*diffFile

	for _, f := range files {
		kept := *f
		kept.Hunks, kept.Adds, kept.Dels = nil, 0, 0

		for _, h := range f.Hunks {
			c := cropHunk(h, from, to)
			if c == nil {
				continue
			}
			kept.Hunks = append(kept.Hunks, c)
			kept.Adds += c.Adds
			kept.Dels += c.Dels
		}

		if len(kept.Hunks) > 0 {
			out = append(out, &kept)
		}
	}
	return out
}

// cropHunk is cropLines for one hunk; nil if nothing is left.
func cropHunk(h *hunk, from, to int) *hunk {
	c := *h
	c.Lines, c.Adds, c.Dels, c.NewStart = nil, 0, 0, 0

	// A removed line belongs where it sits on the new side: right
	// after the last new-side line before it
	at := h.NewStart - 1

	for _, l := range h.Lines {
		pos := l.New
		if l.Kind == '-' {
			pos = at + 1
		} else {
			at = l.New
		}
		if pos < from || pos > to {
			continue
		}

		if c.NewStart == 0 {
			c.NewStart = pos
		}
		c.Lines = append(c.Lines, l)
		switch l.Kind {
		case '+':
			c.Adds++
		case '-':
			c.Dels++
		}
	}

	if len(c.Lines) == 0 {
		return nil
	}
	return &c
}

// gitDiff runs `git diff` with args (revisions, `--`, paths) here.
func gitDiff(ctx context.Context, unified int, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"diff",
		"--no-color", "--no-ext-diff", "--find-renames",
		fmt.Sprintf("-U%d", unified)}, args...)...)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// ---- Rendering ----

// JetBrains Mono 2.304 (OFL, fonts/OFL.txt), embedded: no font to
// install, same look on every machine
var (
	//go:embed fonts/JetBrainsMono-Regular.ttf
	monoRegular []byte

	//go:embed fonts/JetBrainsMono-Bold.ttf
	monoBold []byte
)

// Layout, in pixels, for a 1280-wide frame: ~100 columns of 20 px mono
const (
	frameW, frameH = 1280, 960

	margin   = 24
	fontSize = 20
	rowH     = 28
	fileRowH = 52
	hunkRowH = 38
	gapH     = 14
	tabWidth = 4
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
)

func rgb(hex uint32) color.RGBA {
	return color.RGBA{uint8(hex >> 16), uint8(hex >> 8), uint8(hex), 0xff}
}

// renderDiff draws files as one PNG, 1280 wide and at least 960 tall:
// a header per file, a numbered header per hunk, then the lines.
func renderDiff(files []*diffFile, out string) error {
	regular, err := loadFace(monoRegular)
	if err != nil {
		return err
	}
	bold, err := loadFace(monoBold)
	if err != nil {
		return err
	}

	r := &diffRenderer{face: regular, bold: bold}
	r.adv = font.MeasureString(regular, "M").Round()

	// Gutter: 5-digit line number, space, +/- marker, space
	r.codeX = margin + 8*r.adv
	r.cols = (frameW - r.codeX - margin) / r.adv

	// ---- Measure, then draw ----

	height := r.layout(files, nil)
	img := image.NewRGBA(image.Rect(0, 0, frameW, max(height, frameH)))
	draw.Draw(img, img.Bounds(), image.NewUniform(colBG), image.Point{}, draw.Src)
	r.layout(files, img)

	f, err := os.Create(out)
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

func loadFace(ttf []byte) (font.Face, error) {
	parsed, err := opentype.Parse(ttf)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{
		Size: fontSize, DPI: 72, Hinting: font.HintingFull})
}

// diffRenderer lays rows out top to bottom; with a nil image it only
// measures.
type diffRenderer struct {
	face, bold font.Face
	adv        int // one column, px
	codeX      int // where code starts
	cols       int // code columns before wrapping
}

// layout walks every row; it draws into img unless nil, and returns
// the total height.
func (r *diffRenderer) layout(files []*diffFile, img *image.RGBA) int {
	y := margin

	for _, f := range files {
		// ---- File header ----

		if img != nil {
			fill(img, 0, y, frameW, fileRowH, colFileBG)
			x := r.text(img, r.bold, margin, y, fileRowH, f.Path, colText)
			stats := fmt.Sprintf("  +%d -%d", f.Adds, f.Dels)
			if f.Status != "" {
				stats = "  " + f.Status + stats
			}
			r.text(img, r.face, x, y, fileRowH, stats, colDim)
		}
		y += fileRowH

		if f.Binary {
			if img != nil {
				r.text(img, r.face, margin, y, rowH, "binary file", colDim)
			}
			y += rowH + gapH
			continue
		}

		lexer := chroma.Coalesce(lexerFor(f.Path))

		for _, h := range f.Hunks {
			// ---- Hunk header ----

			if img != nil {
				fill(img, 0, y, frameW, hunkRowH, colHunkBG)
				label := fmt.Sprintf("#%d  line %d", h.Index, h.NewStart)
				if h.Context != "" {
					label += "  " + h.Context
				}
				r.text(img, r.bold, margin, y, hunkRowH,
					clip(label, (frameW-2*margin)/r.adv), colHunkText)
			}
			y += hunkRowH

			// ---- Lines, wrapped ----

			for _, l := range h.Lines {
				rows := wrapTokens(highlight(lexer, l.Text), r.cols)
				if img != nil {
					r.codeLine(img, y, l, rows)
				}
				y += rowH * len(rows)
			}
		}

		y += gapH
	}

	return y + margin
}

// codeLine draws one diff line: tinted background, gutter, code rows.
func (r *diffRenderer) codeLine(img *image.RGBA, y int, l diffLine,
	rows [][]run) {

	h := rowH * len(rows)
	num, mark, markCol := l.New, " ", colDim

	switch l.Kind {
	case '+':
		fill(img, 0, y, frameW, h, colAddBG)
		mark, markCol = "+", colAdd
	case '-':
		fill(img, 0, y, frameW, h, colDelBG)
		num, mark, markCol = l.Old, "-", colDel
	}

	r.text(img, r.face, margin, y, rowH, fmt.Sprintf("%5d", num), colDim)
	r.text(img, r.face, margin+6*r.adv, y, rowH, mark, markCol)

	for i, row := range rows {
		x := r.codeX
		for _, t := range row {
			x = r.text(img, r.face, x, y+i*rowH, rowH, t.text, t.col)
		}
	}
}

// text draws s vertically centered in a row of height h at y, and
// returns the x after it.
func (r *diffRenderer) text(img *image.RGBA, face font.Face, x, y, h int,
	s string, c color.Color) int {

	m := face.Metrics()
	base := y + (h+m.Ascent.Round()-m.Descent.Round())/2

	d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: face,
		Dot: fixed.P(x, base)}
	d.DrawString(s)
	return d.Dot.X.Round()
}

func fill(img *image.RGBA, x, y, w, h int, c color.Color) {
	draw.Draw(img, image.Rect(x, y, x+w, y+h), image.NewUniform(c),
		image.Point{}, draw.Src)
}

// clip cuts s to n runes, marking the cut.
func clip(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// ---- Highlighting ----

// run is a stretch of text in one color.
type run struct {
	text string
	col  color.Color
}

var codeStyle = styles.Get("github-dark")

// lexerFor picks a lexer by file name, plain text otherwise.
func lexerFor(path string) chroma.Lexer {
	if l := lexers.Match(filepath.Base(path)); l != nil {
		return l
	}
	return lexers.Fallback
}

// highlight colors one line of code. Lines are lexed alone, so a
// string or comment spanning lines may lose its color: fine on video.
func highlight(lexer chroma.Lexer, line string) []run {
	line = strings.ReplaceAll(line, "\t", strings.Repeat(" ", tabWidth))

	it, err := lexer.Tokenise(nil, line)
	if err != nil {
		return []run{{line, colText}}
	}

	var runs []run
	for _, tok := range it.Tokens() {
		text := strings.TrimRight(tok.Value, "\n")
		if text == "" {
			continue
		}

		col := color.Color(colText)
		if c := codeStyle.Get(tok.Type).Colour; c.IsSet() {
			col = color.RGBA{c.Red(), c.Green(), c.Blue(), 0xff}
		}
		runs = append(runs, run{text, col})
	}
	return runs
}

// wrapTokens splits runs into rows of at most cols runes; an empty
// line is one empty row.
func wrapTokens(runs []run, cols int) [][]run {
	rows := [][]run{nil}
	used := 0

	for _, t := range runs {
		rest := []rune(t.text)
		for len(rest) > 0 {
			if used == cols {
				rows = append(rows, nil)
				used = 0
			}

			n := min(len(rest), cols-used)
			last := len(rows) - 1
			rows[last] = append(rows[last], run{string(rest[:n]), t.col})
			rest, used = rest[n:], used+n
		}
	}

	return rows
}

// ---- demo diff ----

// cmdDiff renders `git diff ARGS` to a PNG and lists its hunks, so a
// scene can pick them.
func cmdDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	out := fs.String("o", "", "PNG to write (default ~/tmp/demo/diff-<time>.png)")
	unified := fs.Int("U", 3, "context lines around changes")
	picks := fs.String("hunks", "", "hunk numbers to keep, e.g. 1,3 (default all)")
	lines := fs.String("lines", "", "new-side line range to keep, e.g. 120-160")

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: demo diff [flags] "+
			"[git diff args: REV, REV...REV, -- PATHS]\n"+
			"  git flags after a first --: demo diff -- --cached -- dir/\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	hunks, err := parseHunkList(*picks)
	if err != nil {
		return err
	}

	span, err := parseLineRange(*lines)
	if err != nil {
		return err
	}

	path := *out
	if path == "" {
		dir, err := scratchDir()
		if err != nil {
			return err
		}
		path = filepath.Join(dir, "diff-"+time.Now().Format("150405")+".png")
	}

	files, err := diffImage(context.Background(), DiffSpec{Args: fs.Args(),
		Hunks: hunks, Lines: span, Context: *unified}, path)
	if err != nil {
		return err
	}

	fmt.Println(path)
	for _, f := range files {
		for _, h := range f.Hunks {
			label := fmt.Sprintf("#%d %s:%d", h.Index, f.Path, h.NewStart)
			if h.Context != "" {
				label += "  " + h.Context
			}
			fmt.Printf("%s  +%d -%d\n", label, h.Adds, h.Dels)
		}
	}
	return nil
}

// DiffSpec is a diff scene: `git diff Args`, optionally only Hunks,
// then only new-side Lines [from, to].
type DiffSpec struct {
	Args    []string `json:"args"`
	Hunks   []int    `json:"hunks,omitempty"`
	Lines   []int    `json:"lines,omitempty"`
	Context int      `json:"context,omitempty"` // default 3
}

// diffImage renders spec to a PNG at out, and returns what it drew.
func diffImage(ctx context.Context, spec DiffSpec, out string) ([]*diffFile,
	error) {

	if spec.Context == 0 {
		spec.Context = 3
	}

	text, err := gitDiff(ctx, spec.Context, spec.Args)
	if err != nil {
		return nil, err
	}

	files, err := parseDiff(text)
	if err != nil {
		return nil, err
	}

	files = pickHunks(files, spec.Hunks)
	if len(spec.Lines) == 2 {
		files = cropLines(files, spec.Lines[0], spec.Lines[1])
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("git diff %s: nothing to show",
			strings.Join(spec.Args, " "))
	}

	return files, renderDiff(files, out)
}

// parseLineRange parses "120-160"; empty is no range.
func parseLineRange(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}

	a, b, found := strings.Cut(s, "-")
	from, err1 := strconv.Atoi(a)
	to, err2 := strconv.Atoi(b)
	if !found || err1 != nil || err2 != nil || from < 1 || to < from {
		return nil, fmt.Errorf("-lines %q: want FROM-TO, e.g. 120-160", s)
	}
	return []int{from, to}, nil
}

// parseHunkList parses "1,3,4".
func parseHunkList(s string) ([]int, error) {
	var out []int
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' }) {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 1 {
			return nil, fmt.Errorf("-hunks %q: want numbers like 1,3", s)
		}
		out = append(out, n)
	}
	return out, nil
}
