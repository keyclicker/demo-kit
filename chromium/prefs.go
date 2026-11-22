package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// quietPrefs turn off the password manager's bubbles: "Save password?"
// and the breach-check warning, which grabs input after a login and
// fails clicks silently. Paths are dotted keys in Default/Preferences.
var quietPrefs = map[string]bool{
	"credentials_enable_service":              false,
	"profile.password_manager_enabled":        false,
	"profile.password_manager_leak_detection": false,
}

// quietPasswords merges quietPrefs into profile's Default/Preferences,
// creating it on first run. Call only while the browser is closed: it
// rewrites the file on exit.
func quietPasswords(profile string) error {
	path := filepath.Join(profile, "Default", "Preferences")

	prefs := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &prefs); err != nil {
			return err
		}
	}

	for key, value := range quietPrefs {
		setPath(prefs, key, value)
	}

	out, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	return os.WriteFile(path, out, 0o600)
}

// setPath sets a dotted key ("a.b.c") in nested maps, replacing any
// non-map on the way.
func setPath(m map[string]any, key string, value any) {
	for {
		head, rest, nested := strings.Cut(key, ".")
		if !nested {
			m[head] = value
			return
		}

		next, ok := m[head].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[head] = next
		}
		m, key = next, rest
	}
}
