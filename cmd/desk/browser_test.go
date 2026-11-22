package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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

func TestWidevineSeedAndHarvest(t *testing.T) {
	cacheDir = t.TempDir()

	// A first temp profile downloaded a CDM: harvest caches it
	first := t.TempDir()
	cdm := filepath.Join(first, "WidevineCdm")
	os.MkdirAll(filepath.Join(cdm, "4.10.1", "_platform_specific"), 0o700)
	os.WriteFile(filepath.Join(cdm, "4.10.1", "manifest.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(cdm, widevineHint),
		[]byte(`{"Path":"`+filepath.Join(cdm, "4.10.1")+`"}`), 0o600)

	if err := harvestWidevine(first); err != nil {
		t.Fatal(err)
	}
	if v := widevineVersion(widevineCache()); v != "4.10.1" {
		t.Fatalf("cache version = %q, want 4.10.1", v)
	}

	// The next one gets a copy, its hint pointing at its own
	next := t.TempDir()
	seedWidevine(next)

	data, _ := os.ReadFile(filepath.Join(next, "WidevineCdm", widevineHint))
	var hint struct{ Path string }
	json.Unmarshal(data, &hint)
	if want := filepath.Join(next, "WidevineCdm", "4.10.1"); hint.Path != want {
		t.Errorf("seeded hint = %q, want %q", hint.Path, want)
	}
	if _, err := os.Stat(filepath.Join(hint.Path, "manifest.json")); err != nil {
		t.Errorf("seeded CDM missing: %v", err)
	}
}

func TestSeedWidevineEmptyCache(t *testing.T) {
	cacheDir = t.TempDir()

	profile := t.TempDir()
	seedWidevine(profile)

	if _, err := os.Stat(filepath.Join(profile, "WidevineCdm")); err == nil {
		t.Error("seeded a CDM from an empty cache")
	}
}
