package main

import "testing"

func TestShouldMaximize(t *testing.T) {
	cases := map[string]bool{
		"Org.chromium.Chromium":  true,
		"Chromium-browser":       true,
		"libreoffice-writer":     true,
		"Gimp":                   true,
		"XTerm":                  true,
		"com.mitchellh.ghostty":  true,
		"XClock":                 false,
		"missioncenter":          false,
		"libreofficefake":        false,
		"org.chromium.chromiumx": false,
	}

	for class, want := range cases {
		if got := shouldMaximize(class); got != want {
			t.Errorf("shouldMaximize(%q) = %v, want %v", class, got, want)
		}
	}
}
