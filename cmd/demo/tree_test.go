package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTreeLines(t *testing.T) {
	root := newNode()
	for _, f := range []string{"b.go", "a/x.go", "a/deep/y.go", "c/z.go"} {
		root.add(f)
	}

	got := strings.Join(treeLines(root, "", 2), "\n")
	want := strings.Join([]string{
		"├── a/",
		"│   ├── deep/" + treeNote + "…1 file",
		"│   └── x.go",
		"├── c/",
		"│   └── z.go",
		"└── b.go",
	}, "\n")
	if got != want {
		t.Fatalf("tree:\n%s\nwant:\n%s", got, want)
	}
}

func TestReadTreeOutsideGit(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"src/main.go", ".hidden/x", "README.md"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), nil, 0o644)
	}

	l, err := readTree(context.Background(), dir, 0)
	if err != nil {
		t.Fatal(err)
	}

	var lines []string
	for _, cl := range l.Files[0].Hunks[0].Lines {
		lines = append(lines, cl.Text)
	}
	got := strings.Join(lines, "\n")
	if strings.Contains(got, "hidden") || !strings.Contains(got, "main.go") ||
		l.Files[0].Status != "2 files" {
		t.Fatalf("tree (%s):\n%s", l.Files[0].Status, got)
	}

	// Drawn like code, marks included
	if _, err := drawListing(l, highlight{Focus: []int{2, 3},
		Marks: []string{"main.go"}}); err != nil {
		t.Fatal(err)
	}
}

func TestTreeSpecCheck(t *testing.T) {
	for _, c := range []CodeSpec{
		{Tree: "a", File: "b"},
		{Tree: "a", Split: true},
		{File: "a", Depth: 2},
		{Tree: "a", Depth: -1},
	} {
		if c.check() == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	if (CodeSpec{Tree: "a", Depth: 2}).check() != nil {
		t.Error("rejected a plain tree")
	}
}

// Outside git, a symlinked dir is walked where it points
func TestReadTreeSymlink(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "real", "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "real", "sub", "a.go"), nil, 0o644)
	os.Symlink("real", filepath.Join(dir, "link"))

	l, err := readTree(context.Background(), filepath.Join(dir, "link"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if s := l.Files[0].Status; s != "1 file" {
		t.Fatalf("status %q, want the target's 1 file", s)
	}
}

// A directory git ignores is walked: git lists nothing in it
func TestReadTreeIgnoredDir(t *testing.T) {
	dir := t.TempDir()
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Skip("no git")
	}
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "build", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "build", "x", "out.o"), nil, 0o644)

	l, err := readTree(context.Background(), filepath.Join(dir, "build"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if s := l.Files[0].Status; s != "1 file" {
		t.Fatalf("status %q", s)
	}
}
