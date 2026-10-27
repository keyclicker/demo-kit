package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIsBrowser(t *testing.T) {
	const profile = "/home/u/.local/share/agent-chromium"
	dir := "--user-data-dir=" + profile

	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"browser", []string{"/app/chromium/chrome", "--enable-features=X", dir}, true},
		{"renderer", []string{"/app/chromium/chrome", "--type=renderer", dir}, false},
		{"bwrap wrapper", []string{"bwrap", "--args", "71", "--", "chromium", dir}, false},
		{"other profile", []string{"/app/chromium/chrome", dir + "-test"}, false},
		{"shell quoting it", []string{"zsh", "-c", "pkill -f " + dir}, false},
		{"empty", nil, false},
	}

	for _, c := range cases {
		if got := isBrowser(c.argv, profile); got != c.want {
			t.Errorf("%s: isBrowser = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseCmdline(t *testing.T) {
	cases := map[string]int{
		"bwrap\x00--\x00chromium\x00":                  3,
		"/app/chromium/chrome --type=zygote --x=1\x00": 3, // retitled
		"": 0,
	}

	for raw, want := range cases {
		if got := parseCmdline([]byte(raw)); len(got) != want {
			t.Errorf("parseCmdline(%q) = %q, want %d args", raw, got, want)
		}
	}
}

func TestQuietPasswordsKeepsOtherPrefs(t *testing.T) {
	profile := t.TempDir()
	path := filepath.Join(profile, "Default", "Preferences")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{"profile":{"name":"Agent"},"x":1}`), 0o600)

	if err := quietPasswords(profile); err != nil {
		t.Fatal(err)
	}

	var prefs struct {
		Service *bool `json:"credentials_enable_service"`
		X       int   `json:"x"`
		Profile struct {
			Name    string `json:"name"`
			Manager *bool  `json:"password_manager_enabled"`
			Leak    *bool  `json:"password_manager_leak_detection"`
		} `json:"profile"`
	}
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &prefs); err != nil {
		t.Fatal(err)
	}

	if prefs.X != 1 || prefs.Profile.Name != "Agent" {
		t.Errorf("lost existing prefs: %s", data)
	}
	for name, v := range map[string]*bool{
		"service": prefs.Service, "manager": prefs.Profile.Manager,
		"leak": prefs.Profile.Leak,
	} {
		if v == nil || *v {
			t.Errorf("%s not set to false: %s", name, data)
		}
	}
}

func TestQuietPasswordsFreshProfile(t *testing.T) {
	if err := quietPasswords(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
