package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserProfile(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	const agent = "/home/u/.local/share/agent-chromium"
	dir := "--user-data-dir=" + agent

	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"agent browser", []string{"/app/chromium/chrome", "--x", dir}, agent},
		{"user's own", []string{"/app/chromium/chrome", "--x"}, mainProfile()},
		{"renderer", []string{"/app/chromium/chrome", "--type=renderer", dir}, ""},
		{"bwrap wrapper", []string{"bwrap", "--", "chromium", dir}, ""},
		{"shell quoting it", []string{"zsh", "-c", "pkill -f " + dir}, ""},
		{"empty", nil, ""},
	}

	for _, c := range cases {
		if got := browserProfile(c.argv); got != c.want {
			t.Errorf("%s: browserProfile = %q, want %q", c.name, got, c.want)
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
