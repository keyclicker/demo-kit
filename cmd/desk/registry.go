package main

import (
	"encoding/json"
	"errors"
	"fmt"
	flag "github.com/spf13/pflag"
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

// Desktop is one noVNC-reachable display, as `desk ls --json` prints it.
type Desktop struct {
	Display string `json:"display"`
	Token   string `json:"token"`

	// ViewOnly marks desk's own desktops: agents drive them over X,
	// humans only watch.
	ViewOnly bool `json:"viewOnly"`

	// Owner is the agent that created a desk desktop; nil for :0.
	Owner *Owner `json:"owner,omitempty"`

	// Recording is the display's running desk rec, if any.
	Recording *Recording `json:"recording,omitempty"`
}

func unitName(n int) string  { return fmt.Sprintf("desk-%d", n) }
func tokenPath(n int) string { return filepath.Join(stateDir, strconv.Itoa(n)) }

// tokenFile renders the websockify TokenFile entry for display n.
func tokenFile(n int, owner Owner) string {
	return fmt.Sprintf("%s\n%s\n%d: 127.0.0.1:%d\n",
		managedMark, ownerLine(owner), n, basePort+n)
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

// unitAlive reports whether a user unit is starting or running.
func unitAlive(unit string) bool {
	out, _ := exec.Command("systemctl", "--user", "show",
		"--property=ActiveState", "--value", unit).Output()

	switch strings.TrimSpace(string(out)) {
	case "active", "activating", "reloading":
		return true
	}

	return false
}

// ---- Commands ----

// cmdNew starts desk-N on the lowest free display and prints ":N" once
// the desktop is ready for clients. It belongs to the calling agent,
// records itself (unless --no-rec), and goes away when that agent exits.
func cmdNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	size := fs.String("size", "1280x960", "screen size, WxH")
	noRec := fs.Bool("no-rec", false,
		"don't record; desk rec start when you want to")
	fs.Parse(args)

	owner, err := callerOwner()
	if err != nil {
		return err
	}

	if !sizeRE.MatchString(*size) {
		return fmt.Errorf("--size %q: want WxH, e.g. 1280x960", *size)
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
		self, "run", fmt.Sprintf("--owner=%d:%d", owner.PID, owner.Start),
		strconv.Itoa(n), *size)
	start.Stderr = os.Stderr

	if err := start.Run(); err != nil {
		return fmt.Errorf("start %s: %w (see journalctl --user -u %s)",
			unit, err, unit)
	}

	fmt.Printf(":%d\n", n)

	if *noRec {
		return nil
	}

	// The same recorder as `desk rec start`; no video is no reason to
	// lose the desktop
	video := recordingPath(n)
	if err := startRec(n, owner, video); err != nil {
		fmt.Fprintf(os.Stderr, "desk: not recording: %v\n", err)
		return nil
	}

	fmt.Fprintf(os.Stderr, "recording: %s (saved by desk rm)\n", video)
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

	me, _ := callerOwner()

	for _, d := range desks {
		var line string

		switch {
		case !d.ViewOnly && d.Owner == nil:
			line = "user, interactive"
		case !d.ViewOnly && d.Owner.Same(me):
			line = fmt.Sprintf("user, claimed by %s (yours)", d.Owner)
		case !d.ViewOnly:
			line = fmt.Sprintf("user, claimed by %s", d.Owner)
		case d.Owner == nil:
			line = "desk, owner unknown"
		case d.Owner.Same(me):
			line = fmt.Sprintf("%s (yours)", d.Owner)
		default:
			line = d.Owner.String()
		}

		if r := d.Recording; r != nil {
			line += ", " + r.state()
		}

		fmt.Printf("%-4s %s\n", d.Display, line)
	}

	return nil
}

// cmdRemove stops desk displays. Only the caller's own, unless --force:
// parallel agents must not pull each other's desktops away.
func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	all := fs.Bool("all", false, "stop all of your desk displays")
	force := fs.Bool("force", false, "also stop other agents' displays")
	fs.Parse(args)

	desks, err := list()
	if err != nil {
		return err
	}

	me, err := callerOwner()
	if err != nil {
		return err
	}

	// Desk displays by number; a nil owner predates ownership
	owned := map[int]Desktop{}
	for _, d := range desks {
		if n, err := parseDisplay(d.Token); err == nil && d.ViewOnly {
			owned[n] = d
		}
	}

	mine := func(o *Owner) bool { return o != nil && o.Same(me) }

	// ---- Pick displays ----

	var targets []int

	if *all {
		for n, d := range owned {
			if *force || mine(d.Owner) {
				targets = append(targets, n)
			}
		}
	}

	for _, arg := range fs.Args() {
		n, err := parseDisplay(arg)
		if err != nil {
			return err
		}

		d, ok := owned[n]
		if !ok {
			return fmt.Errorf(":%d: no such desk display", n)
		}

		o := d.Owner
		if !*force && !mine(o) {
			owner := "an unknown owner"
			if o != nil {
				owner = o.String()
			}
			return fmt.Errorf(":%d belongs to %s, not you (%s); "+
				"--force to stop it anyway", n, owner, me)
		}

		targets = append(targets, n)
	}

	if len(targets) == 0 {
		if *all {
			return nil
		}
		return errors.New("nothing to remove: pass :N... or --all")
	}

	// ---- Stop ----

	// Recordings stop first (After=), so videos are final before Xvnc
	// goes. Listed too, so stop waits for them.
	units := []string{"--user", "stop"}
	for _, n := range targets {
		if owned[n].Recording != nil {
			units = append(units, recUnitName(n))
		}
		units = append(units, unitName(n))
	}

	// Blocks until each unit is down, video finalized
	stop := exec.Command("systemctl", units...)
	stop.Stderr = os.Stderr

	if err := stop.Run(); err != nil {
		return err
	}

	for _, n := range targets {
		if r := owned[n].Recording; r != nil {
			printRecording(n, r.Video)
		}
	}

	return nil
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
			if err != nil || !unitAlive(unitName(n)) {
				os.Remove(path)
				continue
			}
		}

		desk := Desktop{Display: ":" + token, Token: token, ViewOnly: managed}
		for _, line := range strings.Split(string(data), "\n") {
			if o, ok := parseOwnerLine(line); ok {
				desk.Owner = &o
			}
		}

		if n, err := strconv.Atoi(token); err == nil {
			desk.Recording = readRec(n)
		}

		// A user display's owner is the agent holding its claim
		if n, err := strconv.Atoi(token); err == nil && !managed {
			desk.Owner = readClaim(n)
		}

		desks = append(desks, desk)
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
			unitAlive(unitName(n)) || unitAlive(recUnitName(n))

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
