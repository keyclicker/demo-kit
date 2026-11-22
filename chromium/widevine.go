package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ===== Widevine =====
//
// Chromium downloads the Widevine CDM ~1 min into a profile's first
// launch and loads it only on the next: a fresh temp profile would
// never play DRM. So keep the newest CDM a temp profile downloaded in
// a cache, and copy it into each new one.
//
// Layout, same in a profile and the cache:
//
//	WidevineCdm/
//	  4.10.3112.0/                             the CDM
//	  latest-component-updated-widevine-cdm    {"Path": "<abs path>"}

const widevineHint = "latest-component-updated-widevine-cdm"

func widevineCache() string {
	return filepath.Join(xdgDir("XDG_CACHE_HOME", ".cache"),
		"agent-chromium", "WidevineCdm")
}

// widevineVersion returns the CDM version a WidevineCdm dir's hint
// points at, "" if none is there.
func widevineVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, widevineHint))
	if err != nil {
		return ""
	}

	var hint struct{ Path string }
	if json.Unmarshal(data, &hint) != nil || hint.Path == "" {
		return ""
	}

	version := filepath.Base(hint.Path)
	if _, err := os.Stat(filepath.Join(dir, version)); err != nil {
		return ""
	}

	return version
}

// copyWidevine copies version's CDM from src into a new dst, with a
// hint naming it at final: dst's path once in place.
func copyWidevine(src, dst, final, version string) error {
	err := os.CopyFS(filepath.Join(dst, version),
		os.DirFS(filepath.Join(src, version)))
	if err != nil {
		return err
	}

	hint, err := json.Marshal(struct{ Path string }{
		filepath.Join(final, version),
	})
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dst, widevineHint), hint, 0o600)
}

// seedWidevine copies the cached CDM into a fresh profile. Best
// effort: without it the profile just can't play DRM.
func seedWidevine(profile string) {
	version := widevineVersion(widevineCache())
	if version == "" {
		return
	}

	dst := filepath.Join(profile, "WidevineCdm")
	err := copyWidevine(widevineCache(), dst, dst, version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-chromium: no Widevine this run:", err)
	}
}

// harvestWidevine caches profile's CDM if it's not the cached one: the
// first temp profile ever, or Chromium updated it.
func harvestWidevine(profile string) error {
	src := filepath.Join(profile, "WidevineCdm")
	version := widevineVersion(src)
	if version == "" || version == widevineVersion(widevineCache()) {
		return nil
	}

	// Build it aside, then swap: seeds never read a half-copied CDM
	parent := filepath.Dir(widevineCache())
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "WidevineCdm.new-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if err := copyWidevine(src, tmp, widevineCache(), version); err != nil {
		return err
	}

	os.RemoveAll(widevineCache())
	return os.Rename(tmp, widevineCache())
}
