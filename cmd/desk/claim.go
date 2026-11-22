package main

import (
	"errors"
	"fmt"
	flag "github.com/spf13/pflag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/screensaver"
	"github.com/jezek/xgb/xproto"
)

// errUserActive makes `desk claim` exit 3: the agent must ask first.
var errUserActive = errors.New("user active")

// claimPath holds an agent's claim on a user display. A dotfile, so
// `desk ls` skips it; websockify reads it but finds only comments.
func claimPath(n int) string {
	return filepath.Join(stateDir, fmt.Sprintf(".claim-%d", n))
}

// readClaim returns display n's live claim, or nil. A claim whose
// owner exited is stale: remove it.
func readClaim(n int) *Owner {
	data, err := os.ReadFile(claimPath(n))
	if err != nil {
		return nil
	}

	for _, line := range strings.Split(string(data), "\n") {
		if o, ok := parseOwnerLine(line); ok && o.Alive() {
			return &o
		}
	}

	os.Remove(claimPath(n))
	return nil
}

// parseUserDisplay accepts a display the user owns: listed in stateDir
// and not one of desk's own.
func parseUserDisplay(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(s, ":"))
	if err != nil {
		return 0, fmt.Errorf("%q: not a display", s)
	}

	data, err := os.ReadFile(filepath.Join(stateDir, strconv.Itoa(n)))
	if err != nil || strings.Contains(string(data), managedMark) {
		return 0, fmt.Errorf(":%d: not a user display (see desk ls)", n)
	}

	return n, nil
}

// ---- Commands ----

// cmdClaim gives the caller exclusive use of a user display (:0). It
// goes through silently once the user has been idle long enough; else
// the agent has to ask, then retry with --force.
func cmdClaim(args []string) error {
	fs := flag.NewFlagSet("claim", flag.ExitOnError)
	force := fs.Bool("force", false, "claim even though the user is active")
	idleMin := fs.Duration("idle", 10*time.Minute,
		"user idle time that makes asking unnecessary")
	fs.Parse(args)

	n, err := parseUserDisplay(displayArg(fs))
	if err != nil {
		return err
	}

	me, err := callerOwner()
	if err != nil {
		return err
	}

	unlock, err := lockState()
	if err != nil {
		return err
	}
	defer unlock()

	// ---- Other agents ----

	if o := readClaim(n); o != nil {
		if o.Same(me) {
			fmt.Printf(":%d is already yours\n", n)
			return nil
		}
		return fmt.Errorf(":%d is claimed by %s; wait or use a desk "+
			"display", n, o)
	}

	// ---- The user ----

	if !*force {
		idle, err := idleTime(n)
		if err != nil {
			return err
		}
		if idle < *idleMin {
			fmt.Fprintf(os.Stderr, "desk: the user is active on :%d "+
				"(last input %s ago): ask them first, then "+
				"`desk claim --force`\n", n, idle.Round(time.Second))
			return errUserActive
		}
	}

	data := []byte(ownerLine(me) + "\n")
	if err := os.WriteFile(claimPath(n), data, 0o644); err != nil {
		return err
	}

	fmt.Printf(":%d is yours until `desk release` or you exit\n", n)
	return nil
}

// cmdRelease drops the caller's claim on a user display.
func cmdRelease(args []string) error {
	fs := flag.NewFlagSet("release", flag.ExitOnError)
	force := fs.Bool("force", false, "drop another agent's claim")
	fs.Parse(args)

	n, err := parseUserDisplay(displayArg(fs))
	if err != nil {
		return err
	}

	me, err := callerOwner()
	if err != nil {
		return err
	}

	o := readClaim(n)
	switch {
	case o == nil:
		return nil
	case !o.Same(me) && !*force:
		return fmt.Errorf(":%d is claimed by %s, not you; --force to "+
			"drop it anyway", n, o)
	}

	return os.Remove(claimPath(n))
}

// displayArg is the one optional positional argument, default ":0".
func displayArg(fs *flag.FlagSet) string {
	if fs.NArg() > 0 {
		return fs.Arg(0)
	}
	return ":0"
}

// idleTime asks display n's X server how long ago its last input was,
// from any source: keyboard, mouse, VNC, Moonlight.
func idleTime(n int) (time.Duration, error) {
	conn, err := xgb.NewConnDisplay(":" + strconv.Itoa(n))
	if err != nil {
		return 0, fmt.Errorf(":%d: %w", n, err)
	}
	defer conn.Close()

	if err := screensaver.Init(conn); err != nil {
		return 0, err
	}

	root := xproto.Setup(conn).DefaultScreen(conn).Root
	info, err := screensaver.QueryInfo(conn, xproto.Drawable(root)).Reply()
	if err != nil {
		return 0, err
	}

	return time.Duration(info.MsSinceUserInput) * time.Millisecond, nil
}
