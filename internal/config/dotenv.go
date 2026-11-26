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
// the TTS key (tts.key_env).
func EnvPath() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}

	return filepath.Join(filepath.Dir(path), ".env"), nil
}

// Secret returns the value of the secret variable name: the
// environment's when it sets one, else that of .env beside the config
// file. ok is false when neither does.
//
// .env's values never enter the process environment, so nothing a
// command spawns sees them: a demo's term scene running `env` would
// draw them into a shared video, and desk hands its environment to
// Xvnc, the window manager, Chromium and ffmpeg.
func (c Config) Secret(name string) (value string, ok bool) {
	if value, ok := os.LookupEnv(name); ok {
		return value, true
	}

	value, ok = c.secrets[name]
	return value, ok
}

// readEnv reads the .env file's KEY=VALUEs. Lines may start with
// "export "; values may be quoted; # starts a comment line. A missing
// file is fine; one others can read is refused, as it holds secrets.
func readEnv() (map[string]string, error) {
	path, err := EnvPath()
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s holds secrets but others can read it: "+
			"chmod 600 %s", path, path)
	}

	secrets := map[string]string{}

	lines := bufio.NewScanner(f)
	for n := 1; lines.Scan(); n++ {
		line := strings.TrimSpace(lines.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("%s:%d: want KEY=VALUE", path, n)
		}

		secrets[key] = unquote(strings.TrimSpace(value))
	}

	return secrets, lines.Err()
}

// unquote strips one pair of matching quotes around a value.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}
