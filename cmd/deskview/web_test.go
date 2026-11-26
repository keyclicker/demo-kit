package main

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// importRe matches a module import's specifier: `from "x"`, `import "x"`
// and `import("x")`.
var importRe = regexp.MustCompile(`(?:\bfrom|\bimport)\s*\(?\s*["']([^"']+)["']`)

// TestImportsResolve checks that every relative import in the page and
// noVNC finds its file in the embedded web/: the page uses only part of
// noVNC (scripts/update-novnc.sh), and a newer one may reach further.
func TestImportsResolve(t *testing.T) {
	var checked int

	err := fs.WalkDir(web, "web", func(file string, d fs.DirEntry,
		err error) error {

		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(file, ".js") &&
			!strings.HasSuffix(file, ".html") {
			return nil
		}

		src, err := fs.ReadFile(web, file)
		if err != nil {
			return err
		}

		for _, m := range importRe.FindAllSubmatch(src, -1) {
			spec := string(m[1])
			if !strings.HasPrefix(spec, ".") {
				continue // not a file: a word in a comment, say
			}

			target := path.Join(path.Dir(file), spec)
			if _, err := fs.Stat(web, target); err != nil {
				t.Errorf("%s imports %s: not embedded", file, spec)
			}
			checked++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The page's own import, at least, or the pattern is broken
	if checked == 0 {
		t.Fatal("no imports found")
	}
}
