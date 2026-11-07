package main

import (
	"context"
	_ "embed"
	"fmt"
	flag "github.com/spf13/pflag"
	"image"
	"image/color"
	"image/draw"
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
)

// Code is drawn, not recorded: a source file or a git diff becomes a
// 1280-wide PNG, which a scene pages through. A highlight fades every
// line but a range, and marks text inside it, so narration can point
// at what it talks about.

// ---- Listing ----

// listing is what gets drawn: one or more files of numbered lines.
type listing struct {
	Files []*codeFile
	Diff  bool   // +/- markers, hunk headers, stats
	Split bool   // a diff side by side: old left, new right
	Tree  bool   // a file tree (tree.go), colored by entry kind
	Align string // where a short one sits (lift)
}

// codeFile is one file: of a diff, or a plain source excerpt (one
// hunk, all context lines).
type codeFile struct {
	Path       string
	Status     string // "", "new", "deleted", "renamed"
	Binary     bool
	Hunks      []*hunk
	Adds, Dels int

	// Whole is a plain source's every line, however it's cropped: the
	// header counts them, and the frame around a short range shows them
	Whole *hunk
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

// CodeSpec is what a code scene shows: File (plain source), Diff
// (`git diff` args), or Tree (a directory, Depth levels deep); then
// optionally only Hunks, and new-side Lines [from, to].
type CodeSpec struct {
	File    string   `json:"file,omitempty"`
	Diff    []string `json:"diff,omitempty"`
	Tree    string   `json:"tree,omitempty"`
	Depth   int      `json:"depth,omitempty"` // tree depth, default 3
	Hunks   []int    `json:"hunks,omitempty"`
	Lines   []int    `json:"lines,omitempty"`
	Context *int     `json:"context,omitempty"` // diff context, default 3

	// Split shows a diff side by side, old left and new right
	Split bool `json:"split,omitempty"`

	// Align places a short listing: "center" (default) or "top"
	Align string `json:"align,omitempty"`
}

// check rejects specs with no source, two, or a bad range.
func (c CodeSpec) check() error {
	sources := btoi(c.File != "") + btoi(c.Diff != nil) + btoi(c.Tree != "")

	switch {
	case sources != 1:
		return fmt.Errorf("code: needs exactly one of file, diff, tree")
	case c.Diff == nil && len(c.Hunks) > 0:
		return fmt.Errorf("code: hunks need a diff")
	case c.Diff == nil && c.Split:
		return fmt.Errorf("code: split needs a diff")
	case c.Tree == "" && c.Depth != 0, c.Depth < 0:
		return fmt.Errorf("code: depth needs a tree, and to be positive")
	case c.Context != nil && (c.Diff == nil || *c.Context < 0):
		return fmt.Errorf("code: context needs a diff, and to be 0 or more")
	}
	if err := checkAlign(c.Align); err != nil {
		return err
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
// File and Tree are relative to the current directory.
func (c CodeSpec) load(ctx context.Context) (*listing, error) {
	var l *listing
	var err error

	switch {
	case c.File != "":
		l, err = readSource(c.File)
	case c.Tree != "":
		l, err = readTree(ctx, c.Tree, c.Depth)
	default:
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
	l.Align = c.Align
	return l, nil
}

// texts is every text the listing shows: paths, hunk contexts, lines.
func (l *listing) texts() []string {
	var out []string
	for _, f := range l.Files {
		out = append(out, f.Path)
		for _, h := range f.Hunks {
			out = append(out, h.Context)
			for _, cl := range h.Lines {
				out = append(out, cl.Text)
			}
		}
	}
	return out
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

	file := &codeFile{Path: homePath(path), Hunks: []*hunk{h}, Whole: h}
	return &listing{Files: []*codeFile{file}}, nil
}

// shownLines puts a plain source's shown lines against its length:
// "312 lines" whole, "lines 40-60 of 312" cropped.
func shownLines(f *codeFile) string {
	lines, total := f.Hunks[0].Lines, len(f.Whole.Lines)
	first, last := lines[0].Pos, lines[len(lines)-1].Pos

	if first == 1 && last == total {
		return fmt.Sprintf("%d lines", total)
	}
	return fmt.Sprintf("lines %d-%d of %d", first, last, total)
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

// defaultContext is a diff's context lines when the spec sets none.
const defaultContext = 3

// readDiff runs `git diff` with spec's args and context, and keeps the
// picked hunks. Paths come raw and a/, b/-prefixed, whatever the
// user's git config says.
func readDiff(ctx context.Context, c CodeSpec) (*listing, error) {
	unified := defaultContext
	if c.Context != nil {
		unified = *c.Context
	}

	cmd := exec.CommandContext(ctx, "git", append([]string{
		"-c", "core.quotePath=false", "diff",
		"--no-color", "--no-ext-diff", "--find-renames",
		"--src-prefix=a/", "--dst-prefix=b/",
		fmt.Sprintf("-U%d", unified)}, c.Diff...)...)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s: %w", strings.Join(c.Diff, " "), err)
	}

	files, err := parseDiff(string(out))
	if err != nil {
		return nil, err
	}
	return &listing{Files: pickHunks(files, c.Hunks), Diff: true,
		Split: c.Split}, nil
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
				if newN == 0 {
					cl.Pos = oldN // a deleted file has no new side
				}
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
			file.Path = unquotePath(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "Binary files "):
			file.Binary = true

		case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "):
			p := strings.TrimPrefix(unquotePath(line[4:]), "b/")
			if line[0] == '+' && p != "/dev/null" {
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

			// No new lines (-U0): git names the line before the removal
			if newLeft == 0 && newN > 0 {
				newN++
			}

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

// gitPath takes the b/ side of "diff --git a/X b/X", quoted or not.
func gitPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.LastIndex(rest, ` "b/`); i >= 0 &&
		strings.HasSuffix(rest, `"`) {
		return strings.TrimPrefix(unquotePath(rest[i+1:]), "b/")
	}
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return rest
}

// unquotePath reads a path git quoted C-style ("a\"b", "\303\251"):
// with core.quotePath off, still those with quotes, backslashes or
// control characters. Other paths come back as they are.
func unquotePath(s string) string {
	if !strings.HasPrefix(s, `"`) {
		return s
	}
	if p, err := strconv.Unquote(s); err == nil {
		return p
	}
	return s
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

// Layout, in pixels, for a 1280-wide frame
const (
	margin   = 24
	fileRowH = 52
	hunkRowH = 38
	gapH     = 14
	tabWidth = 4
	focusBar = 6
)

// metrics size the code text. Advances are whole pixels at these sizes
// (JetBrains Mono is 0.6 em wide), so columns map to x exactly.
type metrics struct {
	fontSize float64
	rowH     int
	margin   int // inside each pane
}

var (
	unifiedMetrics = metrics{fontSize: 20, rowH: 28, margin: margin}
	splitMetrics   = metrics{fontSize: 15, rowH: 22, margin: 12}

	// Bigger sizes for listings that fit a frame with room to spare
	largeMetrics = []metrics{
		{fontSize: 30, rowH: 42, margin: margin},
		{fontSize: 25, rowH: 35, margin: margin},
	}
)

// render is a drawn listing: the image, where each line landed, and
// each file header's y (sticky: shot).
type render struct {
	Img   *image.RGBA
	Heads []int
	rows  map[int][2]int // Pos → [top, bottom) y
}

// focusY is the view top that centers lines [from, to] (centerY) in
// the frame under a pinned header.
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
	return centerY(top-margin-fileRowH, bottom, r.Img.Bounds().Dy())
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

	// A split keeps its size; else the largest that fits one frame
	// and wraps no more lines than the default, or the default, paged
	runs := lexListing(l)
	var p *painter
	if l.Split {
		p = newPainter(l, runs, hl, splitMetrics)
	} else {
		p = newPainter(l, runs, hl, unifiedMetrics)
		p.layout(nil)
		for _, m := range largeMetrics {
			big := newPainter(l, runs, hl, m)
			if big.layout(nil) <= frameH && big.wraps <= p.wraps {
				p = big
				break
			}
		}
	}

	// ---- Measure, then draw ----

	height := p.layout(nil)
	if height > maxDrawH {
		return nil, fmt.Errorf("%d px tall, over %d: narrow it with "+
			"lines or hunks", height, maxDrawH)
	}
	p.top = lift(frameH-height, l.Align) // a short one: under its header
	p.img = newCanvas(height)
	p.rows = map[int][2]int{}
	p.layout(p.img)

	for _, mk := range hl.Marks {
		if !p.marked[mk] {
			return nil, fmt.Errorf("mark %q: not in the shown lines", mk)
		}
	}
	if f := hl.Focus; f != nil && !anyIn(p.rows, f[0], f[1]) {
		return nil, fmt.Errorf("focus %d-%d: no shown line there "+
			"(demo code lists them)", f[0], f[1])
	}

	return &render{Img: p.img, Heads: p.heads, rows: p.rows}, nil
}

// newPainter sets a painter up to draw l, colored by runs, with hl at
// m's size: one pane, or a split's two.
func newPainter(l *listing, runs colors, hl highlight, m metrics) *painter {
	regular := monoFace(false, m.fontSize)
	p := &painter{listing: l, runs: runs, face: regular,
		bold: monoFace(true, m.fontSize), hl: hl, rowH: m.rowH,
		marked: map[string]bool{}}
	p.adv = font.MeasureString(regular, "M").Round()

	if l.Split {
		half := frameW / 2
		p.panes = []pane{p.newPane(0, half, m.margin, true),
			p.newPane(half, half, m.margin, false)}
	} else {
		p.panes = []pane{p.newPane(0, frameW, m.margin, false)}
	}
	return p
}

// anyIn reports whether a drawn line sits in [from, to].
func anyIn(rows map[int][2]int, from, to int) bool {
	for pos := range rows {
		if pos >= from && pos <= to {
			return true
		}
	}
	return false
}

// pane is a column of code: the whole frame, or one side of a split.
type pane struct {
	x, w   int  // left edge and width
	margin int  // before the gutter, and after the code
	codeX  int  // where code starts
	cols   int  // code columns before wrapping
	old    bool // numbers context lines by the old file (a split's left)
}

// newPane lays out a pane: margin, 5-digit line number, marker, code.
func (p *painter) newPane(x, w, margin int, old bool) pane {
	codeX := x + margin + 8*p.adv
	return pane{x: x, w: w, margin: margin, codeX: codeX,
		cols: (x + w - codeX - margin) / p.adv, old: old}
}

// painter lays rows out top to bottom; with a nil image it only
// measures.
type painter struct {
	listing    *listing
	runs       colors
	face, bold font.Face
	hl         highlight
	marked     map[string]bool // marks found, for drawListing's check

	adv   int // one column, px
	rowH  int
	panes []pane
	top   int // px between the first header and its code (lift)
	wraps int // rows past a line's first, counted by layout

	img   *image.RGBA
	rows  map[int][2]int
	heads []int // file headers' y, as drawn
}

// layout walks every row; it draws into img unless nil, and returns
// the total height.
func (p *painter) layout(img *image.RGBA) int {
	y := margin
	p.wraps, p.heads = 0, nil

	for i, f := range p.listing.Files {
		if img != nil {
			p.fileHeader(img, y, f)
			p.heads = append(p.heads, y)
		}
		y += fileRowH
		if i == 0 {
			y += p.top // the header stays on top, the code moves
		}

		if f.Binary {
			if img != nil {
				drawText(img, p.face, margin, y, p.rowH, "binary file", colDim)
			}
			y += p.rowH + gapH
			continue
		}

		for _, h := range f.Hunks {
			if p.listing.Diff {
				if img != nil {
					p.hunkHeader(img, y, h)
				}
				y += hunkRowH
			}

			top := y
			for _, row := range p.rowsOf(h) {
				y += p.row(img, y, h, p.runs[h], row)
			}
			if img != nil && f.Whole != nil {
				p.surround(img, f, h, top, y)
			}
		}

		y += gapH
	}

	return y + margin
}

// rowsOf lists what each row shows, as indexes into h.Lines per pane
// (-1: empty). Unified: one line per row. Split: context on both
// sides; a run of removals beside the run of additions after it.
func (p *painter) rowsOf(h *hunk) [][]int {
	var rows [][]int

	if len(p.panes) == 1 {
		for i := range h.Lines {
			rows = append(rows, []int{i})
		}
		return rows
	}

	for i := 0; i < len(h.Lines); {
		if h.Lines[i].Kind == ' ' {
			rows = append(rows, []int{i, i})
			i++
			continue
		}

		var dels, adds []int
		for ; i < len(h.Lines) && h.Lines[i].Kind == '-'; i++ {
			dels = append(dels, i)
		}
		for ; i < len(h.Lines) && h.Lines[i].Kind == '+'; i++ {
			adds = append(adds, i)
		}

		for k := range max(len(dels), len(adds)) {
			row := []int{-1, -1}
			if k < len(dels) {
				row[0] = dels[k]
			}
			if k < len(adds) {
				row[1] = adds[k]
			}
			rows = append(rows, row)
		}
	}
	return rows
}

// row draws one row of panes at y (measures only, with a nil image)
// and returns its height: the tallest of its wrapped lines.
func (p *painter) row(img *image.RGBA, y int, h *hunk, runs [][]run,
	row []int) int {

	wrapped := make([][][]run, len(row))
	height := p.rowH
	for k, li := range row {
		if li >= 0 {
			wrapped[k] = wrapTokens(runs[li], p.panes[k].cols)
			height = max(height, p.rowH*len(wrapped[k]))
			p.wraps += len(wrapped[k]) - 1
		}
	}

	if img == nil {
		return height
	}

	for k, li := range row {
		pn := p.panes[k]
		if li < 0 {
			fill(img, pn.x, y, pn.w, height, colFileBG)
			continue
		}

		l := h.Lines[li]
		p.codeLine(img, pn, y, height, l, wrapped[k])

		// Focus finds a row by the new side's position, else the old
		if k == len(row)-1 || row[len(row)-1] < 0 {
			p.rows[l.Pos] = mergeSpan(p.rows, l.Pos, y, y+height)
		}
	}

	// The split's seam
	if len(row) == 2 {
		fill(img, p.panes[1].x-1, y, 2, height, colBG)
	}
	return height
}

// mergeSpan widens pos's span to cover [top, bottom): a removed line
// shares its position with the new line after it.
func mergeSpan(rows map[int][2]int, pos, top, bottom int) [2]int {
	if s, ok := rows[pos]; ok {
		return [2]int{min(s[0], top), max(s[1], bottom)}
	}
	return [2]int{top, bottom}
}

// fileHeader draws a file's path, then its status and +/- counts in
// their colors, or the shown line range.
func (p *painter) fileHeader(img *image.RGBA, y int, f *codeFile) {
	fill(img, 0, y, frameW, fileRowH, colFileBG)
	x := drawText(img, p.bold, margin, y, fileRowH, f.Path, colText)

	if p.listing.Tree {
		drawText(img, p.face, x, y, fileRowH, "  "+f.Status, colDim)
		return
	}

	if !p.listing.Diff {
		drawText(img, p.face, x, y, fileRowH, "  "+shownLines(f), colDim)
		return
	}

	if f.Status != "" {
		x = drawText(img, p.face, x, y, fileRowH, "  "+f.Status, colDim)
	}
	x = drawText(img, p.face, x, y, fileRowH, fmt.Sprintf("  +%d", f.Adds),
		colAdd)
	drawText(img, p.face, x, y, fileRowH, fmt.Sprintf(" -%d", f.Dels), colDel)
}

// hunkHeader draws a diff hunk's numbered header row.
func (p *painter) hunkHeader(img *image.RGBA, y int, h *hunk) {
	fill(img, 0, y, frameW, hunkRowH, colHunkBG)

	label := fmt.Sprintf("#%d  line %d", h.Index, h.NewStart)
	if h.Context != "" {
		label += "  " + h.Context
	}
	drawText(img, p.bold, margin, y, hunkRowH,
		clip(label, (frameW-2*margin)/p.adv), colHunkText)
}

// codeLine draws one line in a pane, in a row of height h: tinted
// background, gutter, marks, code rows, and focus (a bar on it, or a
// fade over it).
func (p *painter) codeLine(img *image.RGBA, pn pane, y, h int, l codeLine,
	rows [][]run) {

	focus := p.hl.Focus
	inFocus := focus == nil || (l.Pos >= focus[0] && l.Pos <= focus[1])

	num, mark, markCol := l.New, " ", colDim
	if pn.old {
		num = l.Old
	}

	switch l.Kind {
	case '+':
		fill(img, pn.x, y, pn.w, h, colAddBG)
		mark, markCol = "+", colAdd
	case '-':
		fill(img, pn.x, y, pn.w, h, colDelBG)
		num, mark, markCol = l.Old, "-", colDel
	}

	gx := pn.x + pn.margin
	drawText(img, p.face, gx, y, p.rowH, fmt.Sprintf("%5d", num), colDim)
	drawText(img, p.face, gx+6*p.adv, y, p.rowH, mark, markCol)

	if inFocus {
		p.marks(img, pn, y, l.Text)
	}

	for i, row := range rows {
		x := pn.codeX
		for _, t := range row {
			x = drawText(img, p.face, x, y+i*p.rowH, p.rowH, t.text, t.col)
		}
	}

	// ---- Focus ----

	switch {
	case focus == nil:
	case inFocus:
		fill(img, pn.x, y, focusBar, h, colFocus)
	default:
		fade(img, image.Rect(pn.x, y, pn.x+pn.w, y+h))
	}
}

// surround fills the frame around a plain source's shown rows [top,
// bottom) with its lines before and after them, faded, and vi's ~ past
// either end of the file: the range sits in its file, as in an editor.
func (p *painter) surround(img *image.RGBA, f *codeFile, h *hunk,
	top, bottom int) {

	first, last := h.Lines[0].Pos, h.Lines[len(h.Lines)-1].Pos
	above := img.SubImage(image.Rect(0, margin+fileRowH, frameW, top))
	below := img.SubImage(image.Rect(0, bottom, frameW, img.Rect.Max.Y))

	// Drawn into the sub-images: rows cut by an edge clip there
	for _, side := range []struct {
		img      *image.RGBA
		y, n, dn int
	}{
		{above.(*image.RGBA), top - p.rowH, first - 1, -1},
		{below.(*image.RGBA), bottom, last + 1, 1},
	} {
		r := side.img.Rect
		for y, n := side.y, side.n; y+p.rowH > r.Min.Y && y < r.Max.Y; {
			p.unshown(side.img, y, f, n)
			y, n = y+side.dn*p.rowH, n+side.dn
		}
		fade(side.img, r)
	}
}

// unshown draws f's line n at y, one row (the rest clipped), or vi's ~
// where the file has no line n.
func (p *painter) unshown(img *image.RGBA, y int, f *codeFile, n int) {
	pn := p.panes[0]
	gx := pn.x + pn.margin

	if n < 1 || n > len(f.Whole.Lines) {
		drawText(img, p.face, gx, y, p.rowH, "~", colDim)
		return
	}

	drawText(img, p.face, gx, y, p.rowH, fmt.Sprintf("%5d", n), colDim)
	x := pn.codeX
	for _, t := range wrapTokens(p.runs[f.Whole][n-1], pn.cols)[0] {
		x = drawText(img, p.face, x, y, p.rowH, t.text, t.col)
	}
}

// marks boxes every occurrence of the highlight's marks in a line's
// text, cell by cell across wrapped rows. Drawn before the text, so
// the text stays on top.
func (p *painter) marks(img *image.RGBA, pn pane, y int, text string) {
	text = strings.ReplaceAll(text, "\t", strings.Repeat(" ", tabWidth))

	for _, m := range p.hl.Marks {
		for at := 0; ; {
			i := strings.Index(text[at:], m)
			if i < 0 {
				break
			}
			p.marked[m] = true

			start := utf8.RuneCountInString(text[:at+i])
			p.markCells(img, pn, y, start, start+utf8.RuneCountInString(m))
			at += i + len(m)
		}
	}
}

// markCells boxes columns [from, to) of a line starting at y.
func (p *painter) markCells(img *image.RGBA, pn pane, y, from, to int) {
	for col := from; col < to; {
		row, c := col/pn.cols, col%pn.cols
		n := min(to-col, pn.cols-c)

		x0 := pn.codeX + c*p.adv - 2
		x1 := pn.codeX + (c+n)*p.adv + 2
		top := y + row*p.rowH + 2

		draw.Draw(img, image.Rect(x0, top, x1, top+p.rowH-4),
			image.NewUniform(colMark), image.Point{}, draw.Over)
		fill(img, x0, top+p.rowH-6, x1-x0, 2, colFocus)

		col += n
	}
}

// ---- Placement ----

// A listing shorter than the frame keeps its first header on top; the
// rest sits a third of the free space down (align "center", the
// optical center: dead center looks sunk), or right under it ("top").
const alignTop, alignCenter = "top", "center"

// checkAlign accepts "", "top" or "center".
func checkAlign(align string) error {
	if align != "" && align != alignTop && align != alignCenter {
		return fmt.Errorf("code: align %q: want %q or %q", align,
			alignTop, alignCenter)
	}
	return nil
}

// lift is how far down align puts a listing's body, given the frame's
// free px.
func lift(free int, align string) int {
	if align == alignTop {
		return 0
	}
	return max(0, free) / 3
}

// ---- Highlighting ----

// run is a stretch of text in one color.
type run struct {
	text string
	col  color.Color
	bold bool // terminal output only: code is drawn in one weight
}

var codeStyle = styles.Get("github-dark")

// lexerFor picks a lexer by file name, plain text otherwise.
func lexerFor(path string) chroma.Lexer {
	if l := lexers.Match(filepath.Base(path)); l != nil {
		return l
	}
	return lexers.Fallback
}

// colors is each hunk's runs, one []run per line, lexed once per
// drawing whatever sizes it tries.
type colors map[*hunk][][]run

// lexListing colors l's hunks. A plain source is lexed whole and its
// shown lines sliced out, so a range starting inside a string or a
// comment keeps its colors; its Whole keeps all of them, for surround.
func lexListing(l *listing) colors {
	runs := colors{}

	for _, f := range l.Files {
		lexer := chroma.Coalesce(lexerFor(f.Path))

		if f.Whole != nil {
			whole := highlightHunk(lexer, f.Whole)
			runs[f.Whole] = whole
			for _, h := range f.Hunks {
				from := h.Lines[0].Pos - 1
				runs[h] = whole[from : from+len(h.Lines)]
			}
			continue
		}

		for _, h := range f.Hunks {
			if l.Tree {
				runs[h] = treeRuns(h)
			} else {
				runs[h] = highlightHunk(lexer, h)
			}
		}
	}
	return runs
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
			out[i] = []run{{text: l, col: colText}}
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
				out[line] = append(out[line], run{text: part, col: col})
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
			piece := t
			piece.text = string(rest[:n])
			rows[last] = append(rows[last], piece)
			rest, used = rest[n:], used+n
		}
	}

	return rows
}

// ---- demo code ----

// cmdCode draws a source file (or with --diff a git diff, with --tree a
// file tree) as a PNG and lists what it drew: a code scene's preview.
func cmdCode(args []string) error {
	fs := flag.NewFlagSet("code", flag.ExitOnError)
	diff := fs.Bool("diff", false, "args are git diff args, not a file")
	split := fs.Bool("split", false, "a diff side by side: old left, new right")
	tree := fs.Bool("tree", false, "the arg is a directory: draw its file tree")
	depth := fs.Int("depth", 0, "tree depth (default 3)")
	out := fs.StringP("out", "o", "", "PNG to write (default ~/tmp/demo/code-<time>.png)")
	unified := fs.IntP("unified", "U", 3, "diff context lines")
	picks := fs.String("hunks", "", "diff hunks to keep, e.g. 1,3 (default all)")
	lines := fs.String("lines", "", "line range to keep, e.g. 120-160")
	focus := fs.String("focus", "", "line range to highlight, e.g. 130-138")
	align := fs.String("align", "", `a short listing: "center" (default, `+
		`a third down) or "top"`)
	var marks []string
	fs.Func("mark", "`text` to box inside the focus (repeatable)",
		func(s string) error { marks = append(marks, s); return nil })

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: demo code [flags] FILE\n"+
			"       demo code --tree [--depth N] [flags] DIR\n"+
			"       demo code --diff [flags] -- [git diff args]\n"+
			"  e.g. demo code --diff -- origin/master...HEAD -- dir/\n")
		fs.PrintDefaults()
	}
	// Flags first: for a diff, what follows goes to git as is, -- and
	// all. A FILE or DIR may have its flags after it
	fs.SetInterspersed(false)
	fs.Parse(args)
	rest := fs.Args()
	if !*diff && len(rest) > 1 {
		fs.SetInterspersed(true)
		fs.Parse(rest[1:])
		rest = append(rest[:1:1], fs.Args()...)
	}

	spec := CodeSpec{Split: *split, Depth: *depth, Align: *align}
	if *diff {
		spec.Context = unified
	}
	if *tree && len(rest) == 1 {
		spec.Tree = rest[0]
	} else if *diff {
		spec.Diff = append([]string{}, rest...)
	} else if len(rest) == 1 {
		spec.File = rest[0]
	} else {
		fs.Usage()
		os.Exit(2)
	}

	var err error
	if spec.Hunks, err = parseList("hunks", *picks); err != nil {
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

	ctx, stop := signalContext()
	defer stop()

	l, err := spec.load(ctx)
	if err != nil {
		return err
	}

	// What's in it first: drawing a long diff takes a few seconds
	printListing(l)
	warnGlyphs(os.Stderr, "code", l.texts())

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

// printListing names what was drawn: numbered hunks, line ranges, or
// for a tree its numbered lines (for focus and lines ranges).
func printListing(l *listing) {
	for _, f := range l.Files {
		for _, h := range f.Hunks {
			first, last := h.Lines[0].Pos, h.Lines[len(h.Lines)-1].Pos
			if l.Tree {
				for _, cl := range h.Lines {
					fmt.Printf("%4d  %s\n", cl.Pos, cl.Text)
				}
				continue
			}
			if !l.Diff {
				fmt.Printf("%s %s\n", f.Path, shownLines(f))
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

// parseList parses "1,3,4", flag --name's value; empty is nil.
func parseList(name, s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		if s == "" {
			break
		}
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 1 {
			return nil, fmt.Errorf("--%s %q: want numbers like 1,3", name, s)
		}
		out = append(out, n)
	}
	return out, nil
}
