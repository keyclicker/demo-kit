// Package paths resolves demo-kit's per-user directories, following the
// XDG Base Directory spec: one rule per kind of file, no detection.
//
//	Config()  $XDG_CONFIG_HOME/demo-kit  (~/.config/demo-kit)
//	Data()    $XDG_DATA_HOME/demo-kit    (~/.local/share/demo-kit)
//	Cache()   $XDG_CACHE_HOME/demo-kit   (~/.cache/demo-kit)
//
// An XDG variable that is unset, empty or relative is ignored, as the
// spec says, and the default under $HOME is used instead.
package paths

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// app names demo-kit's subdirectory in every XDG base directory.
const app = "demo-kit"

// Config returns the directory that holds config.toml.
func Config() (string, error) {
	return base("XDG_CONFIG_HOME", ".config")
}

// Data returns the directory for files worth keeping: recordings and
// demo workspaces by default.
func Data() (string, error) {
	return base("XDG_DATA_HOME", ".local/share")
}

// Cache returns the directory for files that can be rebuilt: the speech
// cache, previews and other scratch.
func Cache() (string, error) {
	return base("XDG_CACHE_HOME", ".cache")
}

// Expand replaces a leading "~" or "~/" in path with the home
// directory. Other paths, "~user" forms included, come back unchanged.
//
// Expand fails only when path needs the home directory and it is
// unknown.
func Expand(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, path[1:]), nil
}

// base returns $env/demo-kit, or ~/fallback/demo-kit when $env is not an
// absolute path.
func base(env, fallback string) (string, error) {
	if dir := os.Getenv(env); filepath.IsAbs(dir) {
		return filepath.Join(dir, app), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("no home directory: set $HOME or $" + env)
	}

	return filepath.Join(home, fallback, app), nil
}
