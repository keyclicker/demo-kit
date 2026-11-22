package main

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
)

const sampleDiff = `diff --git a/a.go b/a.go
index 1..2 100644
--- a/a.go
+++ b/a.go
@@ -1,3 +1,4 @@ package a
 package a
-var x = 1
+var x = 2
+var y = 3
 
@@ -10,2 +11,2 @@ func f() {
---- looks like a header, is content
+--- so is this
 }
diff --git a/new.txt b/new.txt
new file mode 100644
--- /dev/null
+++ b/new.txt
@@ -0,0 +1 @@
+hello
\ No newline at end of file
diff --git a/logo.png b/logo.png
Binary files a/logo.png and b/logo.png differ
`

func TestParseDiff(t *testing.T) {
	files, err := parseDiff(sampleDiff)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("got %d files", len(files))
	}

	a := files[0]
	if a.Path != "a.go" || len(a.Hunks) != 2 || a.Adds != 3 || a.Dels != 2 {
		t.Fatalf("a.go: %+v", a)
	}

	h := a.Hunks[0]
	if h.Index != 1 || h.Context != "package a" || len(h.Lines) != 5 {
		t.Fatalf("hunk 1: %+v", h)
	}
	if l := h.Lines[2]; l.Kind != '+' || l.New != 2 || l.Old != 0 {
		t.Fatalf("hunk 1 line 3: %+v", l)
	}

	// Body lines are counted, never taken for headers
	if h := a.Hunks[1]; h.Lines[0].Kind != '-' ||
		h.Lines[0].Text != "--- looks like a header, is content" {
		t.Fatalf("hunk 2: %+v", h.Lines)
	}

	if n := files[1]; n.Path != "new.txt" || n.Status != "new" ||
		n.Hunks[0].Index != 3 || n.Adds != 1 {
		t.Fatalf("new.txt: %+v", n)
	}
	if !files[2].Binary {
		t.Fatal("logo.png: not binary")
	}
}

func TestPickHunks(t *testing.T) {
	files, _ := parseDiff(sampleDiff)
	got := pickHunks(files, []int{2, 3})

	if len(got) != 2 || len(got[0].Hunks) != 1 || got[0].Hunks[0].Index != 2 ||
		got[0].Adds != 1 || got[0].Dels != 1 || got[1].Path != "new.txt" {
		t.Fatalf("pickHunks: %+v %+v", got[0], got[1])
	}

	// The input stays whole
	if len(files[0].Hunks) != 2 {
		t.Fatal("pickHunks changed its input")
	}
}

func TestWrapTokens(t *testing.T) {
	runs := []run{{text: "abcd", col: colText}, {text: "efg", col: colAdd}}
	rows := wrapTokens(runs, 3)
	var got []string
	for _, r := range rows {
		s := ""
		for _, t := range r {
			s += t.text
		}
		got = append(got, s)
	}
	if len(got) != 3 || got[0] != "abc" || got[1] != "def" || got[2] != "g" {
		t.Fatalf("rows = %q", got)
	}

	if rows := wrapTokens(nil, 3); len(rows) != 1 {
		t.Fatalf("empty line: %d rows", len(rows))
	}
}

func TestDrawListing(t *testing.T) {
	files, _ := parseDiff(sampleDiff)
	r, err := drawListing(&listing{Files: files, Diff: true}, highlight{})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "d.png")
	if err := savePNG(r.Img, out); err != nil {
		t.Fatal(err)
	}

	f, _ := os.Open(out)
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil || cfg.Width != frameW || cfg.Height < frameH {
		t.Fatalf("png %dx%d, %v", cfg.Width, cfg.Height, err)
	}
}

func TestParseHunkList(t *testing.T) {
	if got, err := parseList("hunks", "1, 3"); err != nil || len(got) != 2 ||
		got[1] != 3 {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := parseList("hunks", "0"); err == nil {
		t.Fatal("accepted hunk 0")
	}
}

func TestCropLines(t *testing.T) {
	files, _ := parseDiff(sampleDiff)

	// Hunk 1, new side: 1 package, 2-3 added, 4 blank; "-var x" sits at 2
	got := cropLines(files[:1], 2, 3)
	if len(got) != 1 || len(got[0].Hunks) != 1 {
		t.Fatalf("got %+v", got)
	}
	h := got[0].Hunks[0]
	if h.NewStart != 2 || len(h.Lines) != 3 || h.Adds != 2 || h.Dels != 1 ||
		h.Lines[0].Kind != '-' {
		t.Fatalf("cropped hunk: %+v", h)
	}

	if got := cropLines(files[:1], 100, 200); len(got) != 0 {
		t.Fatalf("out of range kept %+v", got)
	}
}

func TestParseLineRange(t *testing.T) {
	if got, err := parseLineRange("lines", "120-160"); err != nil || got[1] != 160 {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"5", "9-3", "a-b", "0-2"} {
		if _, err := parseLineRange("lines", bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestFocus(t *testing.T) {
	files, _ := parseDiff(sampleDiff)
	l := &listing{Files: files[:1], Diff: true}

	r, err := drawListing(l, highlight{Focus: []int{2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	if y := r.focusY(2, 3); y != 0 {
		t.Fatalf("short listing scrolled to %d", y)
	}

	// Positions are per file: focus on a multi-file listing is refused
	if _, err := drawListing(&listing{Files: files, Diff: true},
		highlight{Focus: []int{1, 2}}); err == nil {
		t.Fatal("focus on 3 files accepted")
	}
}

func TestReadSourceAndHighlight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	os.WriteFile(path, []byte("package a\n\nvar s = `one\ntwo`\n"), 0o644)

	l, err := readSource(path)
	if err != nil {
		t.Fatal(err)
	}
	h := l.Files[0].Hunks[0]
	if len(h.Lines) != 4 || h.Lines[3].Pos != 4 || h.Lines[3].Text != "two`" {
		t.Fatalf("lines: %+v", h.Lines)
	}

	// The raw string's second line keeps the string's color
	runs := highlightHunk(chroma.Coalesce(lexerFor(path)), h)
	str := runs[2][len(runs[2])-1].col
	if len(runs[3]) == 0 || runs[3][0].col != str {
		t.Fatalf("line 4 runs %+v, want string color %v", runs[3], str)
	}
}

func TestCodeSpecCheck(t *testing.T) {
	bad := []CodeSpec{
		{},
		{File: "a", Diff: []string{}},
		{File: "a", Hunks: []int{1}},
		{File: "a", Lines: []int{5, 2}},
		{File: "a", Context: new(int)},
		{Diff: []string{}, Context: new(-1)},
	}
	for _, c := range bad {
		if c.check() == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestMarks(t *testing.T) {
	files, _ := parseDiff(sampleDiff)
	l := &listing{Files: files[:1], Diff: true}

	// Found inside the focus: fine
	if _, err := drawListing(l, highlight{Focus: []int{2, 3},
		Marks: []string{"y = 3"}}); err != nil {
		t.Fatal(err)
	}

	// Only outside it, or nowhere: a typo, so an error
	for _, m := range []string{"package", "nope"} {
		if _, err := drawListing(l, highlight{Focus: []int{2, 3},
			Marks: []string{m}}); err == nil {
			t.Errorf("mark %q accepted", m)
		}
	}
}

func TestSplitRows(t *testing.T) {
	files, _ := parseDiff(sampleDiff)
	h := files[0].Hunks[0] // ' ', '-', '+', '+', ' '

	p := &painter{panes: make([]pane, 2)}
	got := p.rowsOf(h)
	want := [][]int{{0, 0}, {1, 2}, {-1, 3}, {4, 4}}
	if len(got) != len(want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
	for i := range want {
		if got[i][0] != want[i][0] || got[i][1] != want[i][1] {
			t.Fatalf("rows %v, want %v", got, want)
		}
	}
}

func TestSplitDraws(t *testing.T) {
	files, _ := parseDiff(sampleDiff)
	l := &listing{Files: files[:1], Diff: true, Split: true}
	r, err := drawListing(l, highlight{Focus: []int{2, 3},
		Marks: []string{"x = 2"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Img.Bounds().Dx() != frameW {
		t.Fatalf("width %d", r.Img.Bounds().Dx())
	}

	if (CodeSpec{File: "a", Split: true}).check() == nil {
		t.Fatal("split on a plain file accepted")
	}
}

func TestFocusMustMatch(t *testing.T) {
	files, _ := parseDiff(sampleDiff)
	l := &listing{Files: files[:1], Diff: true}
	if _, err := drawListing(l, highlight{Focus: []int{500, 510}}); err == nil {
		t.Fatal("focus past the end accepted")
	}
}

// A replaced block's removed lines share the next new line's position:
// its span must cover them, or a tall focus scrolls past them
func TestRemovedLinesInSpan(t *testing.T) {
	files, _ := parseDiff(sampleDiff)
	r, err := drawListing(&listing{Files: files[:1], Diff: true},
		highlight{})
	if err != nil {
		t.Fatal(err)
	}
	span, one := r.rows[2], r.rows[1] // "-var x = 1", "+var x = 2"
	if span[1]-span[0] != 2*(one[1]-one[0]) {
		t.Fatalf("span of position 2 = %v, want two rows of %v", span, one)
	}
}

// A short listing draws large, its header on top and its code a third
// down (or right under it, aligned top); a long one at the default
// size from the top
func TestListingSize(t *testing.T) {
	short := source("a.go", []codeLine{{Kind: ' ', Text: "package main",
		Pos: 1}})
	r, err := drawListing(short, highlight{})
	if err != nil {
		t.Fatal(err)
	}
	if span := r.rows[1]; span[1]-span[0] != largeMetrics[0].rowH ||
		span[0] < frameH/4 || span[0] > frameH/2 {
		t.Fatalf("short listing's line at %v", span)
	}
	if c := r.Img.RGBAAt(frameW/2, fileRowH/2); c != colFileBG {
		t.Fatalf("header not on top: %v", c)
	}

	short.Align = alignTop
	if r, err = drawListing(short, highlight{}); err != nil {
		t.Fatal(err)
	}
	if span := r.rows[1]; span[0] != bodyTop {
		t.Fatalf("top-aligned line at %v", span)
	}

	var lines []codeLine
	for i := range 40 {
		lines = append(lines, codeLine{Kind: ' ', Text: "x", Pos: i + 1})
	}
	long := source("a.go", lines)
	if r, err = drawListing(long, highlight{}); err != nil {
		t.Fatal(err)
	}
	if span := r.rows[1]; span[1]-span[0] != unifiedMetrics.rowH ||
		span[0] > 2*fileRowH {
		t.Fatalf("long listing's first line at %v", span)
	}
}

func TestDeletedFilePositions(t *testing.T) {
	files, err := parseDiff(`diff --git a/gone.txt b/gone.txt
deleted file mode 100644
--- a/gone.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-one
-two
`)
	if err != nil {
		t.Fatal(err)
	}
	lines := files[0].Hunks[0].Lines
	if lines[0].Pos != 1 || lines[1].Pos != 2 {
		t.Fatalf("positions %d, %d; want old numbers", lines[0].Pos, lines[1].Pos)
	}
}

func TestListingHeightCap(t *testing.T) {
	var lines []codeLine
	for i := 1; i <= 2000; i++ {
		lines = append(lines, codeLine{Kind: ' ', Old: i, New: i, Pos: i})
	}
	if _, err := drawListing(source("big.txt", lines), highlight{}); err == nil {
		t.Fatal("2000 lines drawn")
	}
}

// Paths git still quotes (quotes, backslashes) come out unquoted
func TestQuotedPaths(t *testing.T) {
	files, err := parseDiff(`diff --git "a/say \"hi\".go" "b/say \"hi\".go"
--- "a/say \"hi\".go"
+++ "b/say \"hi\".go"
@@ -1 +1 @@
-a
+b
diff --git "a/\321\204.txt" "b/\321\204.txt"
new file mode 100644
Binary files /dev/null and "b/\321\204.txt" differ
`)
	if err != nil {
		t.Fatal(err)
	}
	if p := files[0].Path; p != `say "hi".go` {
		t.Errorf("+++ path %q", p)
	}
	if p := files[1].Path; p != "ф.txt" {
		t.Errorf("diff --git path %q", p)
	}
}

// With -U0, git names the line before a removal: it sits after it
func TestNoContextRemovalPos(t *testing.T) {
	files, err := parseDiff(`diff --git a/n b/n
--- a/n
+++ b/n
@@ -10 +9,0 @@
-10
`)
	if err != nil {
		t.Fatal(err)
	}
	if l := files[0].Hunks[0].Lines[0]; l.Pos != 10 || l.Old != 10 {
		t.Fatalf("removed line 10: %+v, want Pos 10", l)
	}
}

// readDiff: context 0 is 0, and paths are plain whatever git's config
func TestReadDiffConfig(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	nums := strings.Repeat("x\n", 9) + "gone\n" + strings.Repeat("y\n", 9)
	os.WriteFile(filepath.Join(dir, "файл.txt"), []byte(nums), 0o644)
	git("init", "-q")
	git("add", "-A")
	git("-c", "user.name=t", "-c", "user.email=t@t", "-c",
		"commit.gpgsign=false", "commit", "-qm", "init")
	git("config", "diff.mnemonicPrefix", "true")
	os.WriteFile(filepath.Join(dir, "файл.txt"),
		[]byte(strings.Replace(nums, "gone\n", "", 1)), 0o644)

	t.Chdir(dir)
	l, err := readDiff(context.Background(),
		CodeSpec{Diff: []string{}, Context: new(int)})
	if err != nil {
		t.Fatal(err)
	}

	f := l.Files[0]
	if f.Path != "файл.txt" {
		t.Errorf("path %q", f.Path)
	}
	if lines := f.Hunks[0].Lines; len(lines) != 1 || lines[0].Pos != 10 {
		t.Errorf("-U0: %+v", lines)
	}
}

// A FILE's flags may follow it
func TestCodeFlagsAfterFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.go")
	out := filepath.Join(dir, "a.png")
	os.WriteFile(src, []byte("package a\n\nvar x = 1\n"), 0o644)

	err := cmdCode([]string{src, "--focus", "3-3", "--mark", "x", "-o", out})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}

// source is a plain source listing of lines, as readSource makes one.
func source(path string, lines []codeLine) *listing {
	h := &hunk{Index: 1, OldStart: 1, NewStart: 1, Lines: lines}
	return &listing{Files: []*codeFile{{Path: path, Hunks: []*hunk{h},
		Whole: h}}}
}

// A short range sits in its file: the header counts the file's lines,
// the line before it shows faded, and past the file's start, a ~
func TestSurroundings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644)

	l, err := readSource(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Files = cropLines(l.Files, 2, 2)
	if got := shownLines(l.Files[0]); got != "lines 2-2 of 3" {
		t.Fatalf("header %q", got)
	}

	r, err := drawListing(l, highlight{})
	if err != nil {
		t.Fatal(err)
	}
	span := r.rows[2]
	rowH := span[1] - span[0]
	codeX := margin + 8*(rowH*3/7) // past the gutter at any size
	gutter := func(y int) image.Rectangle {
		return image.Rect(0, y, codeX, y+rowH)
	}
	code := func(y int) image.Rectangle {
		return image.Rect(codeX, y, frameW, y+rowH)
	}

	if y := span[0] - rowH; !inked(r.Img, code(y)) {
		t.Fatal("line 1 not shown above the range")
	}
	if y := span[0] - 2*rowH; !inked(r.Img, gutter(y)) ||
		inked(r.Img, code(y)) {
		t.Fatal("no ~ before the file's start")
	}
	if y := span[1]; !inked(r.Img, code(y)) {
		t.Fatal("line 3 not shown below the range")
	}
}

// inked reports whether any pixel in r differs from the background.
func inked(img *image.RGBA, r image.Rectangle) bool {
	bg := color.RGBAModel.Convert(colBG).(color.RGBA)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.RGBAAt(x, y) != bg {
				return true
			}
		}
	}
	return false
}
