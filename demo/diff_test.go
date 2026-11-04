package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
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
	rows := wrapTokens([]run{{"abcd", colText}, {"efg", colAdd}}, 3)
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

func TestRenderDiff(t *testing.T) {
	files, _ := parseDiff(sampleDiff)
	out := filepath.Join(t.TempDir(), "d.png")
	if err := renderDiff(files, out); err != nil {
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
	if got, err := parseHunkList("1, 3"); err != nil || len(got) != 2 ||
		got[1] != 3 {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := parseHunkList("0"); err == nil {
		t.Fatal("accepted hunk 0")
	}
}
