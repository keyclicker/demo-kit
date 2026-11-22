// Command desk spawns disposable, GPU-accelerated X displays for agents.
//
// Each desktop is a TigerVNC Xvnc server, GPU-accelerated through a DRM
// render node when there is one, with desk.wm on top, run as the
// transient systemd user unit desk-N. Its VNC port is registered as
// token N in $XDG_RUNTIME_DIR/demo-kit, so `desk ls --json` lists it for
// viewers.
// desk-rec-N records a display (desk's own, or :0) to record.dir: by
// default from `desk new` until `desk rm`.
package main

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/keyclicker/demo-kit/internal/config"
	"github.com/keyclicker/demo-kit/internal/paths"
)

// cfg is the config in effect, set once by main.
var cfg config.Config

const usageText = `desk: disposable X displays for agents

  desk new [--size WxH] [--no-rec]   start a recorded desktop, print :N
  desk ls [--json]                   list desktops, owners, recordings
  desk rm [--force] :N... | --all    stop your desktops (--force: anyone's),
                                     print their finished videos (path, URL)

  desk rec start|pause|resume|stop :N
                                     record by hand: pauses, custom files,
                                     :0 (claimed). desk rec -h for details

  desk claim [--force] [:0]          take the user's :0 for yourself
  desk release [:0]                  give it back

  desk config [KEY]                  print the config in effect, or one
                                     key's value

A desktop or claim belongs to the agent that ran desk (the first
non-shell ancestor, or $DESK_OWNER_PID) and ends when that agent exits.
claim exits 3 while the user is active (input in the last 10 min): ask
them, then claim --force. Another agent's claim always blocks.

Oneliner:  DISPLAY=$(desk new) xterm &
`

func main() {
	log.SetFlags(0)
	log.SetPrefix("desk: ")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]

	var err error
	if cfg, err = config.Load(); err != nil {
		log.Fatal(err)
	}

	if stateDir, err = paths.Runtime(); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		log.Fatal(err)
	}

	switch cmd {
	case "new":
		err = cmdNew(args)
	case "ls":
		err = cmdList(args)
	case "rm":
		err = cmdRemove(args)
	case "run":
		// Internal: a desk-N unit's main process and its ExecStopPost
		err = cmdRun(args)
	case "cleanup":
		err = cmdCleanup(args)
	case "claim":
		err = cmdClaim(args)
	case "release":
		err = cmdRelease(args)
	case "rec":
		err = cmdRec(args)
	case "config":
		err = config.Cmd("desk", cfg, args, os.Stdout)
	case "help", "-h", "-help", "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	if errors.Is(err, errUserActive) {
		os.Exit(3)
	}
	if err != nil {
		log.Fatal(err)
	}
}
