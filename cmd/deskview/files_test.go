package main

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// On a real directory, files keep Seek: a video answers a Range
// request, and a type that needs sniffing is sniffed
func TestHideDotsServesRanges(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "demo.mp4"), []byte("0123456789"),
		0o644)
	os.WriteFile(filepath.Join(dir, "notes"), []byte("plain text"), 0o644)

	srv := httptest.NewServer(http.FileServerFS(hideDots{os.DirFS(dir)}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/demo.mp4", nil)
	req.Header.Set("Range", "bytes=2-5")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != http.StatusPartialContent || string(body) != "2345" {
		t.Errorf("Range: %s %q; want 206 \"2345\"", res.Status, body)
	}

	res, err = http.Get(srv.URL + "/notes")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("sniffed type: %s; want 200", res.Status)
	}
}
