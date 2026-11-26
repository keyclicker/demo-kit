package main

import (
	"io/fs"
	"strings"
)

// hideDots is a file system without dot-names: a path with a segment
// starting with "." doesn't exist, and listings leave such entries out.
// It keeps .git, .cache and the like in publish.root private.
type hideDots struct{ fs.FS }

func (h hideDots) Open(name string) (fs.File, error) {
	if hidden(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}

	f, err := h.FS.Open(name)
	if err != nil {
		return nil, err
	}

	// Directories only: every *os.File is a ReadDirFile, and wrapped, a
	// file loses Seek (Range requests, type sniffing)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}

	if dir, ok := f.(fs.ReadDirFile); ok && info.IsDir() {
		return dotlessDir{dir}, nil
	}
	return f, nil
}

// hidden reports whether any segment of a slash path starts with ".".
func hidden(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part != "." && strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

// dotlessDir lists a directory without its dot-names.
type dotlessDir struct{ fs.ReadDirFile }

func (d dotlessDir) ReadDir(n int) ([]fs.DirEntry, error) {
	entries, err := d.ReadDirFile.ReadDir(n)

	kept := entries[:0]
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			kept = append(kept, e)
		}
	}
	return kept, err
}
