package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// xauth gets its commands on stdin, the cookie never in argv: a stub
// xauth records both.
func TestXauthKeepsCookieOutOfArgv(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	stubCommand(t, "xauth", `echo "argv: $*" >>`+log+`; cat >>`+log)

	cookie := "00112233445566778899aabbccddeeff"
	err := xauth("/x/auth", "add :21 MIT-MAGIC-COOKIE-1 "+cookie,
		"remove :22")
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"argv: -q -f /x/auth source -",
		"add :21 MIT-MAGIC-COOKIE-1 " + cookie, "remove :22"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("xauth saw:\n%s\nwant:\n%s", data, strings.Join(want, "\n"))
	}
}

// The real xauth takes `source -`, and fails on a bad command.
func TestXauthSourcesStdin(t *testing.T) {
	if _, err := exec.LookPath("xauth"); err != nil {
		t.Skip("no xauth")
	}

	file := filepath.Join(t.TempDir(), "auth")
	cookie := "00112233445566778899aabbccddeeff"
	if err := xauth(file, "add :21 MIT-MAGIC-COOKIE-1 "+cookie); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("xauth", "-f", file, "list").Output()
	if err != nil || !strings.Contains(string(out), cookie) {
		t.Fatalf("xauth list = %q, %v; want the cookie", out, err)
	}

	if err := xauth(file, "bogus"); err == nil {
		t.Error("a bad command succeeded")
	}
}

// A desk display's cookie is desk's file; the host display's, the
// caller's own.
func TestXauthority(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XAUTHORITY", "/run/user/1000/gdm/Xauthority")
	desk := filepath.Join(home, ".Xauthority")

	for display, want := range map[string]string{
		":21": desk, ":10": desk,
		":0": "/run/user/1000/gdm/Xauthority",
	} {
		if got, err := xauthority(display); err != nil || got != want {
			t.Errorf("xauthority(%q) = %q, %v; want %q", display, got,
				err, want)
		}
	}

	t.Setenv("XAUTHORITY", "")
	if got, _ := xauthority(":0"); got != desk {
		t.Errorf("xauthority(:0) without $XAUTHORITY = %q; want %q", got,
			desk)
	}
}
