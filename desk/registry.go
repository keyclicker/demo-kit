package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// ---- Layout ----

const (
	// stateDir is websockify's TokenFile source: one file per desktop,
	// named after its display number. tmpfiles.d creates it, with the
	// user's own :0 as token 0.
	stateDir = "/run/desk"

	// Display numbers desk may hand out; VNC port is basePort + N.
	firstDisplay = 10
	lastDisplay  = 99
	basePort     = 5900

	// managedMark tags token files desk wrote. websockify skips it as a
	// comment.
	managedMark = "# desk: view-only"
)

var sizeRE = regexp.MustCompile(`^[1-9][0-9]{2,4}x[1-9][0-9]{2,4}$`)

// Desktop is one noVNC-reachable display, as `desk ls -json` prints it.
type Desktop struct {
	Display string `json:"display"`
	Token   string `json:"token"`

	// ViewOnly marks desk's own desktops: agents drive them over X,
	// humans only watch.
	ViewOnly bool `json:"viewOnly"`
}

func unitName(n int) string  { return fmt.Sprintf("desk-%d", n) }
func tokenPath(n int) string { return filepath.Join(stateDir, strconv.Itoa(n)) }

// tokenFile renders the websockify TokenFile entry for display n.
func tokenFile(n int) string {
	return fmt.Sprintf("%s\n%d: 127.0.0.1:%d\n", managedMark, n, basePort+n)
}

// parseDisplay accepts "10" or ":10" within desk's display range.
func parseDisplay(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(s, ":"))
	if err != nil || n < firstDisplay || n > lastDisplay {
		return 0, fmt.Errorf("%q: not a desk display (:%d-:%d)",
			s, firstDisplay, lastDisplay)
	}

	return n, nil
}

// unitAlive reports whether desk-N is starting or running.
func unitAlive(n int) bool {
	out, _ := exec.Command("systemctl", "--user", "show",
		"--property=ActiveState", "--value", unitName(n)).Output()

	switch strings.TrimSpace(string(out)) {
	case "active", "activating", "reloading":
		return true
	}

	return false
}

// ---- Commands ----

// cmdNew starts desk-N on the lowest free display and prints ":N" once
// the desktop is ready for clients.
func cmdNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	size := fs.String("size", "1280x800", "screen size, WxH")
	fs.Parse(args)

	if !sizeRE.MatchString(*size) {
		return fmt.Errorf("-size %q: want WxH, e.g. 1920x1080", *size)
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}

	// Hold the lock until the token exists, so parallel `desk new`s
	// never pick the same display.
	unlock, err := lockState()
	if err != nil {
		return err
	}
	defer unlock()

	n, err := freeDisplay()
	if err != nil {
		return err
	}

	unit := unitName(n)

	// Type=notify: systemd-run returns once `desk run` reports ready
	start := exec.Command("systemd-run", "--user", "--quiet", "--collect",
		"--unit="+unit, fmt.Sprintf("--description=desk :%d", n),
		"--property=Type=notify", "--property=TimeoutStartSec=30",
		fmt.Sprintf("--property=ExecStopPost=%s cleanup %d", self, n),
		self, "run", strconv.Itoa(n), *size)
	start.Stderr = os.Stderr

	if err := start.Run(); err != nil {
		return fmt.Errorf("start %s: %w (see journalctl --user -u %s)",
			unit, err, unit)
	}

	fmt.Printf(":%d\n", n)
	return nil
}

// cmdList prints the desktops noVNC can reach.
func cmdList(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print JSON for the /desktops page")
	fs.Parse(args)

	desks, err := list()
	if err != nil {
		return err
	}

	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(desks)
	}

	for _, d := range desks {
		owner := "user, interactive"
		if d.ViewOnly {
			owner = "desk, view-only"
		}
		fmt.Printf("%-4s %s\n", d.Display, owner)
	}

	return nil
}

// cmdRemove stops the given desk displays, or all of them with -all.
func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	all := fs.Bool("all", false, "stop every desk display")
	fs.Parse(args)

	var units []string

	if *all {
		desks, err := list()
		if err != nil {
			return err
		}
		for _, d := range desks {
			if n, err := parseDisplay(d.Token); err == nil && d.ViewOnly {
				units = append(units, unitName(n))
			}
		}
	}

	for _, arg := range fs.Args() {
		n, err := parseDisplay(arg)
		if err != nil {
			return err
		}
		units = append(units, unitName(n))
	}

	if len(units) == 0 {
		return errors.New("nothing to remove: pass :N... or -all")
	}

	stop := exec.Command("systemctl",
		append([]string{"--user", "stop"}, units...)...)
	stop.Stderr = os.Stderr

	return stop.Run()
}

// ---- Registry ----

// list reads every token file in stateDir, sorted by display. It drops
// desk entries whose unit is gone: a crashed unit leaves its token.
func list() ([]Desktop, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil, err
	}

	desks := []Desktop{}

	for _, e := range entries {
		token := e.Name()
		if e.IsDir() || strings.HasPrefix(token, ".") {
			continue
		}

		path := filepath.Join(stateDir, token)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		managed := strings.Contains(string(data), managedMark)
		if managed {
			n, err := parseDisplay(token)
			if err != nil || !unitAlive(n) {
				os.Remove(path)
				continue
			}
		}

		desks = append(desks, Desktop{
			Display:  ":" + token,
			Token:    token,
			ViewOnly: managed,
		})
	}

	slices.SortFunc(desks, func(a, b Desktop) int {
		x, _ := strconv.Atoi(a.Token)
		y, _ := strconv.Atoi(b.Token)
		return x - y
	})

	return desks, nil
}

// freeDisplay returns the lowest display number nothing else holds.
func freeDisplay() (int, error) {
	for n := firstDisplay; n <= lastDisplay; n++ {
		taken := exists(fmt.Sprintf("/tmp/.X11-unix/X%d", n)) ||
			exists(fmt.Sprintf("/tmp/.X%d-lock", n)) ||
			exists(tokenPath(n)) ||
			unitAlive(n)

		if !taken {
			return n, nil
		}
	}

	return 0, fmt.Errorf("no free display in :%d-:%d", firstDisplay,
		lastDisplay)
}

// lockState takes the exclusive allocation lock in stateDir.
func lockState() (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(stateDir, ".lock"),
		os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}

	return func() { f.Close() }, nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
