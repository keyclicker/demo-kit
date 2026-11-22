package main

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestHideDots(t *testing.T) {
	h := hideDots{fstest.MapFS{
		"demos/demo.mp4":   {},
		".git/config":      {},
		"demos/.draft.mp4": {},
	}}

	for _, name := range []string{".git/config", "demos/.draft.mp4", ".git"} {
		if _, err := h.Open(name); err == nil {
			t.Errorf("Open(%q): no error, want not found", name)
		}
	}

	if _, err := h.Open("demos/demo.mp4"); err != nil {
		t.Errorf("Open(demos/demo.mp4): %v", err)
	}

	for dir, want := range map[string]int{".": 1, "demos": 1} {
		entries, err := fs.ReadDir(h, dir)
		if err != nil || len(entries) != want {
			t.Errorf("ReadDir(%q) = %d entries, %v; want %d", dir,
				len(entries), err, want)
		}
	}
}
