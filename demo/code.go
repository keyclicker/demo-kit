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
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Code is drawn, not recorded: a source file or a git diff becomes a
// 1280-wide PNG, which a scene pages through. A highlight fades every
// line but a range, and marks text inside it, so narration can point
// at what it talks about.

// ---- Listing ----

// listing is what gets drawn: one or more files of numbered lines.
type listing struct {
	Files []*codeFile
	Diff  bool // +/- markers, hunk headers, stats
}

// codeFile is one file: of a diff, or a plain source excerpt (one
// hunk, all context lines).
type codeFile struct {
	Path       string
	Status     string // "", "new", "deleted", "renamed"
	Binary     bool
	Hunks      []*hunk
	Adds, Dels int
}

// hunk is one block of lines. In a diff, Index counts hunks across the
// whole diff, from 1: it's how `hunks` picks them.
type hunk struct {
	Index              int
	OldStart, NewStart int
	Context            string // git's function context after the @@
	Lines              []codeLine
	Adds, Dels         int
}

// codeLine is one line: Kind ' ', '+' or '-'. Pos is where it sits in
// the new file: its own number, or for a removed line, right after the
// new-side line before it. Line ranges (`lines`, `focus`) select by Pos.
type codeLine struct {
	Kind     byte
	Old, New int // 0 where the side has none
	Pos      int
	Text     string
}

// ---- Sources ----

// CodeSpec is what a code scene shows: File (plain source), or Diff
// (`git diff` args); then optionally only Hunks, and new-side Lines
// [from, to].
type CodeSpec struct {
	File    string   `json:"file,omitempty"`
	Diff    []string `json:"diff,omitempty"`
	Hunks   []int    `json:"hunks,omitempty"`
	Lines   []int    `json:"lines,omitempty"`
	Context int      `json:"context,omitempty"` // diff context, default 3

	// Scroll is how the view moves: "" cuts (default), "smooth" eases
	Scroll string `json:"scroll,omitempty"`
}

// check rejects specs with no source, two, or a bad range.
func (c CodeSpec) check() error {
	switch {
	case (c.File == "") == (c.Diff == nil):
		return fmt.Errorf("code: needs exactly one of file, diff")
	case c.File != "" && len(c.Hunks) > 0:
		return fmt.Errorf("code: hunks need a diff")
	case c.Scroll != "" && c.Scroll != "smooth":
		return fmt.Errorf(`code: scroll %q: want "smooth" or nothing`,
			c.Scroll)
	}
	return checkRange("code lines", c.Lines)
}

// checkRange accepts nil or [from, to] with 1 <= from <= to.
func checkRange(name string, r []int) error {
	if r != nil && (len(r) != 2 || r[0] < 1 || r[1] < r[0]) {
		return fmt.Errorf("%s: want [from, to]", name)
	}
	return nil
}

// load reads the spec's code, here: git runs in the current repo, and
// File is relative to the current directory.
func (c CodeSpec) load(ctx context.Context) (*listing, error) {
	var l *listing
	var err error

	if c.File != "" {
		l, err = readSource(c.File)
	} else {
		l, err = readDiff(ctx, c)
	}
	if err != nil {
		return nil, err
	}

	if len(c.Lines) == 2 {
		l.Files = cropLines(l.Files, c.Lines[0], c.Lines[1])
	}
	if len(l.Files) == 0 {
		return nil, fmt.Errorf("code: nothing to show")
	}
	return l, nil
}

// readSource lists a whole file as one hunk of context lines.
func readSource(path string) (*listing, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	h := &hunk{Index: 1, OldStart: 1, NewStart: 1}
	text := strings.TrimSuffix(string(data), "\n")
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		h.Lines = append(h.Lines, codeLine{Kind: ' ', Old: n, New: n,
			Pos: n, Text: line})
	}

	file := &codeFile{Path: homePath(path), Hunks: []*hunk{h}}
	return &listing{Files: []*codeFile{file}}, nil
}

// homePath shortens a path under $HOME to ~/..., for headers.
func homePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if rel, ok := strings.CutPrefix(path, home+"/"); ok {
		return "~/" + rel
	}
	return path
}

// readDiff runs `git diff` with spec's args and context, and keeps the
// picked hunks.
func readDiff(ctx context.Context, c CodeSpec) (*listing, error) {
	if c.Context == 0 {
		c.Context = 3
	}

	cmd := exec.CommandContext(ctx, "git", append([]string{"diff",
		"--no-color", "--no-ext-diff", "--find-renames",
		fmt.Sprintf("-U%d", c.Context)}, c.Diff...)...)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s: %w", strings.Join(c.Diff, " "), err)
	}

	files, err := parseDiff(string(out))
	if err != nil {
		return nil, err
	}
	return &listing{Files: pickHunks(files, c.Hunks), Diff: true}, nil
}

// ---- Diff parsing ----

var hunkRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$`)

// parseDiff reads `git diff` output. Hunk bodies are consumed by their
// line counts, so content that looks like a header stays content.
func parseDiff(text string) ([]*codeFile, error) {
	var files []*codeFile
	var file *codeFile
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
			cl := codeLine{Kind: kind}
			if len(line) > 0 {
				cl.Text = line[1:]
			}

			switch kind {
			case ' ':
				cl.Old, cl.New, cl.Pos = oldN, newN, newN
				oldN, newN, oldLeft, newLeft = oldN+1, newN+1, oldLeft-1, newLeft-1
			case '-':
				cl.Old, cl.Pos = oldN, newN
				oldN, oldLeft = oldN+1, oldLeft-1
				h.Dels++
				file.Dels++
			case '+':
				cl.New, cl.Pos = newN, newN
				newN, newLeft = newN+1, newLeft-1
				h.Adds++
				file.Adds++
			default:
				return nil, fmt.Errorf("hunk #%d: bad line %q", h.Index, line)
			}

			h.Lines = append(h.Lines, cl)
			continue
		}

		// ---- Headers ----

		switch {
		case strings.HasPrefix(line, "diff --git "):
			file = &codeFile{Path: gitPath(line)}
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

// ---- Selection ----

// pickHunks keeps only the listed hunks (all when empty), and drops
// files left with none.
func pickHunks(files []*codeFile, picks []int) []*codeFile {
	if len(picks) == 0 {
		return files
	}

	want := map[int]bool{}
	for _, i := range picks {
		want[i] = true
	}

	return filterHunks(files, func(h *hunk) *hunk {
		if want[h.Index] {
			return h
		}
		return nil
	})
}

// cropLines keeps only lines with Pos in [from, to]: a new file or a
// source file is one hunk, and this cuts the part worth showing.
func cropLines(files []*codeFile, from, to int) []*codeFile {
	return filterHunks(files, func(h *hunk) *hunk {
		c := *h
		c.Lines, c.Adds, c.Dels, c.NewStart = nil, 0, 0, 0

		for _, l := range h.Lines {
			if l.Pos < from || l.Pos > to {
				continue
			}
			if c.NewStart == 0 {
				c.NewStart = l.Pos
			}
			c.Lines = append(c.Lines, l)
			c.Adds += btoi(l.Kind == '+')
			c.Dels += btoi(l.Kind == '-')
		}

		if len(c.Lines) == 0 {
			return nil
		}
		return &c
	})
}

// filterHunks maps every hunk through keep (nil drops it), recounts
// files' stats, and drops files left empty. The input stays whole.
func filterHunks(files []*codeFile, keep func(*hunk) *hunk) []*codeFile {
	var out []*codeFile

	for _, f := range files {
		kept := *f
		kept.Hunks, kept.Adds, kept.Dels = nil, 0, 0

		for _, h := range f.Hunks {
			if k := keep(h); k != nil {
				kept.Hunks = append(kept.Hunks, k)
				kept.Adds += k.Adds
				kept.Dels += k.Dels
			}
		}
		if len(kept.Hunks) > 0 {
			out = append(out, &kept)
		}
	}
	return out
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
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
	focusBar = 6
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

	// Out-of-focus lines fade toward the background
	colFade = color.NRGBA{0x0d, 0x11, 0x17, 0xb0}
)

func rgb(hex uint32) color.RGBA {
	return color.RGBA{uint8(hex >> 16), uint8(hex >> 8), uint8(hex), 0xff}
}

// render is a drawn listing: the image, and where each line landed.
type render struct {
	Img  *image.RGBA
	rows map[int][2]int // Pos → [top, bottom) y
}

// focusY is the scroll offset that centers lines [from, to] in a
// frame, clamped to the image; a range taller than the frame shows
// its top.
func (r *render) focusY(from, to int) int {
	top, bottom := -1, 0
	for pos, span := range r.rows {
		if pos >= from && pos <= to {
			if top < 0 || span[0] < top {
				top = span[0]
			}
			bottom = max(bottom, span[1])
		}
	}
	if top < 0 {
		return 0
	}

	y := (top+bottom)/2 - frameH/2
	if bottom-top > frameH-2*margin {
		y = top - margin
	}
	return min(max(y, 0), r.Img.Bounds().Dy()-frameH)
}

// highlight is what a step points at: Focus lines [from, to] get a
// bar while the rest fade; Marks are text boxed wherever it appears
// (inside Focus, if set).
type highlight struct {
	Focus []int
	Marks []string
}

// drawListing draws l, at least one frame tall, with hl on it. Focus
// positions are per file, so it needs a listing of one file; a mark
// that matches nothing is an error, as it's surely a typo.
func drawListing(l *listing, hl highlight) (*render, error) {
	if hl.Focus != nil && len(l.Files) != 1 {
		return nil, fmt.Errorf("focus needs one file, not %d: narrow "+
			"the diff to a path, or pick hunks", len(l.Files))
	}

	regular, err := loadFace(monoRegular)
	if err != nil {
		return nil, err
	}
	bold, err := loadFace(monoBold)
	if err != nil {
		return nil, err
	}

	p := &painter{listing: l, face: regular, bold: bold, hl: hl,
		marked: map[string]bool{}}
	p.adv = font.MeasureString(regular, "M").Round()

	// Gutter: 5-digit line number, space, +/- marker, space
	p.codeX = margin + 8*p.adv
	p.cols = (frameW - p.codeX - margin) / p.adv

	// ---- Measure, then draw ----

	height := p.layout(nil)
	p.img = image.NewRGBA(image.Rect(0, 0, frameW, max(height, frameH)))
	draw.Draw(p.img, p.img.Bounds(), image.NewUniform(colBG),
		image.Point{}, draw.Src)
	p.rows = map[int][2]int{}
	p.layout(p.img)

	for _, m := range hl.Marks {
		if !p.marked[m] {
			return nil, fmt.Errorf("mark %q: not in the shown lines", m)
		}
	}

	return &render{Img: p.img, rows: p.rows}, nil
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

func loadFace(ttf []byte) (font.Face, error) {
	parsed, err := opentype.Parse(ttf)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{
		Size: fontSize, DPI: 72, Hinting: font.HintingFull})
}

// painter lays rows out top to bottom; with a nil image it only
// measures.
type painter struct {
	listing    *listing
	face, bold font.Face
	hl         highlight
	marked     map[string]bool // marks found, for drawListing's check

	adv   int // one column, px
	codeX int // where code starts
	cols  int // code columns before wrapping

	img  *image.RGBA
	rows map[int][2]int
}

// layout walks every row; it draws into img unless nil, and returns
// the total height.
func (p *painter) layout(img *image.RGBA) int {
	y := margin

	for _, f := range p.listing.Files {
		// ---- File header ----

		if img != nil {
			fill(img, 0, y, frameW, fileRowH, colFileBG)
			x := p.text(img, p.bold, margin, y, fileRowH, f.Path, colText)
			p.text(img, p.face, x, y, fileRowH, "  "+p.fileNote(f), colDim)
		}
		y += fileRowH

		if f.Binary {
			if img != nil {
				p.text(img, p.face, margin, y, rowH, "binary file", colDim)
			}
			y += rowH + gapH
			continue
		}

		lexer := chroma.Coalesce(lexerFor(f.Path))

		for _, h := range f.Hunks {
			if p.listing.Diff {
				if img != nil {
					p.hunkHeader(img, y, h)
				}
				y += hunkRowH
			}

			runs := highlightHunk(lexer, h)
			for li, l := range h.Lines {
				rows := wrapTokens(runs[li], p.cols)
				height := rowH * len(rows)

				if img != nil {
					p.codeLine(img, y, l, rows)
					p.rows[l.Pos] = [2]int{y, y + height}
				}
				y += height
			}
		}

		y += gapH
	}

	return y + margin
}

// fileNote is the dim text after a file's path: diff stats, or the
// shown line range.
func (p *painter) fileNote(f *codeFile) string {
	if !p.listing.Diff {
		lines := f.Hunks[0].Lines
		return fmt.Sprintf("lines %d-%d", lines[0].Pos, lines[len(lines)-1].Pos)
	}

	note := fmt.Sprintf("+%d -%d", f.Adds, f.Dels)
	if f.Status != "" {
		note = f.Status + "  " + note
	}
	return note
}

// hunkHeader draws a diff hunk's numbered header row.
func (p *painter) hunkHeader(img *image.RGBA, y int, h *hunk) {
	fill(img, 0, y, frameW, hunkRowH, colHunkBG)

	label := fmt.Sprintf("#%d  line %d", h.Index, h.NewStart)
	if h.Context != "" {
		label += "  " + h.Context
	}
	p.text(img, p.bold, margin, y, hunkRowH,
		clip(label, (frameW-2*margin)/p.adv), colHunkText)
}

// codeLine draws one line: tinted background, gutter, marks, code
// rows, and focus (a bar on it, or a fade over it).
func (p *painter) codeLine(img *image.RGBA, y int, l codeLine,
	rows [][]run) {

	focus := p.hl.Focus
	inFocus := focus == nil || (l.Pos >= focus[0] && l.Pos <= focus[1])

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

	p.text(img, p.face, margin, y, rowH, fmt.Sprintf("%5d", num), colDim)
	p.text(img, p.face, margin+6*p.adv, y, rowH, mark, markCol)

	if inFocus {
		p.marks(img, y, l.Text)
	}

	for i, row := range rows {
		x := p.codeX
		for _, t := range row {
			x = p.text(img, p.face, x, y+i*rowH, rowH, t.text, t.col)
		}
	}

	// ---- Focus ----

	switch {
	case focus == nil:
	case inFocus:
		fill(img, 0, y, focusBar, h, colFocus)
	default:
		draw.Draw(img, image.Rect(0, y, frameW, y+h),
			image.NewUniform(colFade), image.Point{}, draw.Over)
	}
}

// marks boxes every occurrence of the highlight's marks in a line's
// text, cell by cell across wrapped rows. Drawn before the text, so
// the text stays on top.
func (p *painter) marks(img *image.RGBA, y int, text string) {
	text = strings.ReplaceAll(text, "\t", strings.Repeat(" ", tabWidth))

	for _, m := range p.hl.Marks {
		for at := 0; ; {
			i := strings.Index(text[at:], m)
			if i < 0 {
				break
			}
			p.marked[m] = true

			start := utf8.RuneCountInString(text[:at+i])
			p.markCells(img, y, start, start+utf8.RuneCountInString(m))
			at += i + len(m)
		}
	}
}

// markCells boxes columns [from, to) of a line starting at y.
func (p *painter) markCells(img *image.RGBA, y, from, to int) {
	for col := from; col < to; {
		row, c := col/p.cols, col%p.cols
		n := min(to-col, p.cols-c)

		x0 := p.codeX + c*p.adv - 2
		x1 := p.codeX + (c+n)*p.adv + 2
		top := y + row*rowH + 2

		draw.Draw(img, image.Rect(x0, top, x1, top+rowH-4),
			image.NewUniform(colMark), image.Point{}, draw.Over)
		fill(img, x0, top+rowH-6, x1-x0, 2, colFocus)

		col += n
	}
}

// text draws s vertically centered in a row of height h at y, and
// returns the x after it.
func (p *painter) text(img *image.RGBA, face font.Face, x, y, h int,
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

// highlightHunk colors a hunk's lines, one []run per line. Each side
// is lexed as a whole (new: context + added; old: context + removed),
// so strings and comments spanning lines keep their color.
func highlightHunk(lexer chroma.Lexer, h *hunk) [][]run {
	var newSide, oldSide []string
	for _, l := range h.Lines {
		if l.Kind != '-' {
			newSide = append(newSide, l.Text)
		}
		if l.Kind != '+' {
			oldSide = append(oldSide, l.Text)
		}
	}

	newRuns := highlightLines(lexer, newSide)
	oldRuns := highlightLines(lexer, oldSide)

	out := make([][]run, len(h.Lines))
	var ni, oi int
	for i, l := range h.Lines {
		switch l.Kind {
		case '-':
			out[i] = oldRuns[oi]
		default:
			out[i] = newRuns[ni]
		}
		if l.Kind != '-' {
			ni++
		}
		if l.Kind != '+' {
			oi++
		}
	}
	return out
}

// highlightLines lexes lines as one text and splits the colored runs
// back into lines.
func highlightLines(lexer chroma.Lexer, lines []string) [][]run {
	out := make([][]run, len(lines))
	text := strings.ReplaceAll(strings.Join(lines, "\n"), "\t",
		strings.Repeat(" ", tabWidth))

	it, err := lexer.Tokenise(nil, text)
	if err != nil {
		for i, l := range lines {
			out[i] = []run{{l, colText}}
		}
		return out
	}

	line := 0
	for _, tok := range it.Tokens() {
		col := color.Color(colText)
		if c := codeStyle.Get(tok.Type).Colour; c.IsSet() {
			col = color.RGBA{c.Red(), c.Green(), c.Blue(), 0xff}
		}

		// A token may span lines (block comments, raw strings)
		for i, part := range strings.Split(tok.Value, "\n") {
			if i > 0 {
				line++
			}
			if part != "" && line < len(out) {
				out[line] = append(out[line], run{part, col})
			}
		}
	}
	return out
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

// ---- demo code ----

// cmdCode draws a source file, or with -diff a git diff, as a PNG and
// lists what it drew: the preview of a code scene.
func cmdCode(args []string) error {
	fs := flag.NewFlagSet("code", flag.ExitOnError)
	diff := fs.Bool("diff", false, "args are `git diff` args, not a file")
	out := fs.String("o", "", "PNG to write (default ~/tmp/demo/code-<time>.png)")
	unified := fs.Int("U", 3, "diff context lines")
	picks := fs.String("hunks", "", "diff hunks to keep, e.g. 1,3 (default all)")
	lines := fs.String("lines", "", "line range to keep, e.g. 120-160")
	focus := fs.String("focus", "", "line range to highlight, e.g. 130-138")
	var marks []string
	fs.Func("mark", "text to box inside the focus (repeatable)",
		func(s string) error { marks = append(marks, s); return nil })

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: demo code [flags] FILE\n"+
			"       demo code -diff [flags] -- [git diff args]\n"+
			"  e.g. demo code -diff -- origin/master...HEAD -- dir/\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	spec := CodeSpec{Context: *unified}
	if *diff {
		spec.Diff = fs.Args()
		if spec.Diff == nil {
			spec.Diff = []string{}
		}
	} else if fs.NArg() == 1 {
		spec.File = fs.Arg(0)
	} else {
		fs.Usage()
		os.Exit(2)
	}

	var err error
	if spec.Hunks, err = parseHunkList(*picks); err != nil {
		return err
	}
	if spec.Lines, err = parseLineRange("lines", *lines); err != nil {
		return err
	}
	focusRange, err := parseLineRange("focus", *focus)
	if err != nil {
		return err
	}
	if err := spec.check(); err != nil {
		return err
	}

	l, err := spec.load(context.Background())
	if err != nil {
		return err
	}

	// What's in it first: drawing a long diff takes a few seconds
	printListing(l)

	r, err := drawListing(l, highlight{focusRange, marks})
	if err != nil {
		return err
	}

	path := *out
	if path == "" {
		dir, err := scratchDir()
		if err != nil {
			return err
		}
		path = filepath.Join(dir, "code-"+time.Now().Format("150405")+".png")
	}
	if err := savePNG(r.Img, path); err != nil {
		return err
	}

	fmt.Println(path)
	return nil
}

// printListing names what was drawn: numbered hunks, or line ranges.
func printListing(l *listing) {
	for _, f := range l.Files {
		for _, h := range f.Hunks {
			first, last := h.Lines[0].Pos, h.Lines[len(h.Lines)-1].Pos
			if !l.Diff {
				fmt.Printf("%s lines %d-%d\n", f.Path, first, last)
				continue
			}

			label := fmt.Sprintf("#%d %s lines %d-%d", h.Index, f.Path,
				first, last)
			if h.Context != "" {
				label += "  " + h.Context
			}
			fmt.Printf("%s  +%d -%d\n", label, h.Adds, h.Dels)
		}
	}
}

// parseLineRange parses "120-160"; empty is no range.
func parseLineRange(name, s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}

	a, b, found := strings.Cut(s, "-")
	from, err1 := strconv.Atoi(a)
	to, err2 := strconv.Atoi(b)
	if !found || err1 != nil || err2 != nil || from < 1 || to < from {
		return nil, fmt.Errorf("-%s %q: want FROM-TO, e.g. 120-160", name, s)
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
