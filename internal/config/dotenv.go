package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// EnvPath returns the .env file beside config.toml, for secrets such as
// OPENROUTER_API_KEY.
func EnvPath() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}

	return filepath.Join(filepath.Dir(path), ".env"), nil
}

// loadEnv sets each KEY=VALUE of the .env file that the environment
// doesn't set already: the real environment wins. Lines may start with
// "export "; values may be quoted; # starts a comment line. A missing
// file is fine; one others can read is refused, as it holds secrets.
func loadEnv() error {
	path, err := EnvPath()
	if err != nil {
		return err
	}

	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s holds secrets but others can read it: "+
			"chmod 600 %s", path, path)
	}

	lines := bufio.NewScanner(f)
	for n := 1; lines.Scan(); n++ {
		line := strings.TrimSpace(lines.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return fmt.Errorf("%s:%d: want KEY=VALUE", path, n)
		}

		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, unquote(strings.TrimSpace(value)))
		}
	}

	return lines.Err()
}

// unquote strips one pair of matching quotes around a value.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}
