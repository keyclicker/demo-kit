package main

import (
	"context"
	"fmt"
	"image/color"
	"io/fs"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// A tree is drawn like code: one line per entry, numbered, so a tree
// scene gets lines, focus, marks and steps like any other. Directories
// past the depth fold into one line with their file count.

const (
	defaultDepth = 3

	// treeNote separates an entry from its dim note ("…12 files")
	treeNote = "  "
)

// node is a directory in the tree being built.
type node struct {
	dirs  map[string]*node
	files []string
	count int // files at any depth below
}

func newNode() *node { return &node{dirs: map[string]*node{}} }

// add puts a slash-separated file path under n.
func (n *node) add(path string) {
	n.count++
	dir, rest, found := strings.Cut(path, "/")
	if !found {
		n.files = append(n.files, path)
		return
	}

	child := n.dirs[dir]
	if child == nil {
		child = newNode()
		n.dirs[dir] = child
	}
	child.add(rest)
}

// readTree lists dir as a tree, depth levels deep: what git tracks or
// would track in a repo (ignored files left out), else every file not
// under a dot-name.
func readTree(ctx context.Context, dir string, depth int) (*listing, error) {
	if depth == 0 {
		depth = defaultDepth
	}

	paths, err := treeFiles(ctx, dir)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("tree %s: no files", dir)
	}

	root := newNode()
	for _, f := range paths {
		root.add(f)
	}

	// "." reads as the directory's real name
	if dir, err = filepath.Abs(dir); err != nil {
		return nil, err
	}
	name := filepath.Base(dir) + "/"
	lines := append([]string{name}, treeLines(root, "", depth)...)

	h := &hunk{Index: 1, OldStart: 1, NewStart: 1}
	for i, text := range lines {
		n := i + 1
		h.Lines = append(h.Lines, codeLine{Kind: ' ', Old: n, New: n,
			Pos: n, Text: text})
	}

	file := &codeFile{Path: homePath(dir), Hunks: []*hunk{h},
		Status: fileCount(len(paths))}
	return &listing{Files: []*codeFile{file}, Tree: true}, nil
}

// treeFiles lists files under dir, relative and slash-separated.
func treeFiles(ctx context.Context, dir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-files",
		"--cached", "--others", "--exclude-standard", "-z")
	if out, err := cmd.Output(); err == nil {
		var files []string
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				files = append(files, f)
			}
		}
		return files, nil
	}

	// Not a repo: walk it, skipping dot-names
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return files, err
}

// treeLines draws n's entries, directories first, each sorted by name.
// Directories at the depth limit fold into one line with a count.
func treeLines(n *node, prefix string, depth int) []string {
	dirs := make([]string, 0, len(n.dirs))
	for d := range n.dirs {
		dirs = append(dirs, d)
	}
	slices.Sort(dirs)
	files := slices.Sorted(slices.Values(n.files))

	var lines []string
	total := len(dirs) + len(files)

	for i, d := range dirs {
		branch, next := entryGlyphs(i == total-1)
		child := n.dirs[d]

		if depth <= 1 {
			lines = append(lines, fmt.Sprintf("%s%s%s/%s…%s", prefix,
				branch, d, treeNote, fileCount(child.count)))
			continue
		}
		lines = append(lines, prefix+branch+d+"/")
		lines = append(lines, treeLines(child, prefix+next, depth-1)...)
	}

	for i, f := range files {
		branch, _ := entryGlyphs(len(dirs)+i == total-1)
		lines = append(lines, prefix+branch+f)
	}
	return lines
}

// fileCount counts files in words: "1 file", "12 files".
func fileCount(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// entryGlyphs are an entry's branch, and the prefix its children get.
func entryGlyphs(last bool) (branch, next string) {
	if last {
		return "└── ", "    "
	}
	return "├── ", "│   "
}

// treeRuns colors tree lines: glyphs dim, directories blue, files
// plain, notes dim.
func treeRuns(h *hunk) [][]run {
	out := make([][]run, len(h.Lines))

	for i, l := range h.Lines {
		name := strings.TrimLeft(l.Text, "│├└─ ")
		glyphs := l.Text[:len(l.Text)-len(name)]
		name, note, _ := strings.Cut(name, treeNote)

		col := color.Color(colText)
		if strings.HasSuffix(name, "/") {
			col = colHunkText
		}

		for _, r := range []run{{glyphs, colDim}, {name, col}} {
			if r.text != "" {
				out[i] = append(out[i], r)
			}
		}
		if note != "" {
			out[i] = append(out[i], run{treeNote + note, colDim})
		}
	}
	return out
}
