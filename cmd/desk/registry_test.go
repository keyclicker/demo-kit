package main

import (
	"net"
	"os"
	"path/filepath"
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
	file := tokenFile(12, Owner{PID: 42, Start: 7, Name: "claude"})
	var targets []string

	for _, l := range strings.Split(file, "\n") {
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
	if !strings.Contains(file, managedMark) {
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

// stubCommand puts an executable shell script named name first on PATH.
func stubCommand(t *testing.T, name, script string) {
	t.Helper()

	dir := t.TempDir()
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body),
		0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A failed `systemctl show` says nothing about the unit: list must keep
// the display and its recording; an answer of "inactive" drops both.
func TestListKeepsStateWhenSystemdFails(t *testing.T) {
	stateDir = t.TempDir()
	owner := Owner{PID: 42, Start: 7, Name: "claude"}
	rec := Recording{Video: "/r/a.mp4", Owner: owner}

	register := func() {
		if err := writeToken(21, owner); err != nil {
			t.Fatal(err)
		}
		if err := writeRec(21, rec); err != nil {
			t.Fatal(err)
		}
	}

	// ---- systemd unreachable ----

	register()
	stubCommand(t, "systemctl", "exit 1")

	desks, err := list()
	if err != nil {
		t.Fatal(err)
	}
	if len(desks) != 1 || desks[0].Display != ":21" ||
		desks[0].Recording == nil || *desks[0].Recording != rec {
		t.Fatalf("list() = %+v; want :21, recording", desks)
	}
	if !exists(tokenPath(21)) || !exists(recPath(21)) {
		t.Fatal("state removed after a failed query")
	}

	// ---- units gone ----

	stubCommand(t, "systemctl", "echo inactive")

	if desks, err = list(); err != nil || len(desks) != 0 {
		t.Fatalf("list() = %+v, %v; want none", desks, err)
	}
	if readRec(21) != nil || exists(tokenPath(21)) || exists(recPath(21)) {
		t.Fatal("stale state kept")
	}
}

// sshd's forwarded displays have only a loopback TCP port: a bound
// 6000+N takes N, IPv4 or IPv6 alike.
func TestTCPBound(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1"} {
		l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			t.Logf("%s: %v (skipped)", host, err)
			continue
		}

		port := l.Addr().(*net.TCPAddr).Port
		if !tcpBound(port) {
			t.Errorf("%s:%d bound, tcpBound says free", host, port)
		}

		l.Close()
		if tcpBound(port) {
			t.Errorf("%s:%d closed, tcpBound says bound", host, port)
		}
	}
}
