package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestParseDisplay(t *testing.T) {
	for in, want := range map[string]int{"10": 10, ":10": 10, ":99": 99} {
		if got, err := parseDisplay(in); err != nil || got != want {
			t.Errorf("parseDisplay(%q) = %d, %v; want %d", in, got, err, want)
		}
	}

	// :0 is the user's desktop, never desk's to stop
	for _, in := range []string{":0", "9", "100", "x", ":10.0", ""} {
		if _, err := parseDisplay(in); err == nil {
			t.Errorf("parseDisplay(%q) accepted", in)
		}
	}
}

// websockify's TokenFile skips "#" lines and splits the rest on ":\s"
func TestTokenFileParsesLikeWebsockify(t *testing.T) {
	line := regexp.MustCompile(`^(\w+):\s(127\.0\.0\.1:\d+)$`)
	var targets []string

	for _, l := range strings.Split(tokenFile(12, Owner{PID: 42, Start: 7, Name: "claude"}), "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		m := line.FindStringSubmatch(l)
		if m == nil || m[1] != "12" {
			t.Fatalf("bad token line %q", l)
		}
		targets = append(targets, m[2])
	}

	if len(targets) != 1 || targets[0] != "127.0.0.1:5912" {
		t.Fatalf("targets = %v", targets)
	}
	if !strings.Contains(tokenFile(12, Owner{PID: 42, Start: 7, Name: "claude"}), managedMark) {
		t.Fatal("token file lacks the managed mark")
	}
}

func TestSize(t *testing.T) {
	for _, ok := range []string{"1280x800", "1920x1080", "3840x2160"} {
		if !sizeRE.MatchString(ok) {
			t.Errorf("rejected %q", ok)
		}
	}
	for _, bad := range []string{"1280", "0x0", "1280x800x24", "-1x5", "a x b"} {
		if sizeRE.MatchString(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}
