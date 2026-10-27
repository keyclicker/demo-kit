// Command desk spawns disposable, GPU-accelerated X displays for agents.
//
// Each desktop is a TigerVNC Xvnc server rendering on the iGPU through
// its DRM render node, with xfwm4 on top, run as the transient systemd
// user unit desk-N. Its VNC port is registered as noVNC token N in
// /run/desk, so the /desktops page picks it up as a view-only tab.
package main

import (
	"fmt"
	"log"
	"os"
)

const usageText = `desk: disposable X displays for agents

  desk new [-size WxH]           start a desktop, print its DISPLAY (:N)
  desk ls [-json]                list desktops and their owners
  desk rm [-force] :N... | -all  stop your desktops (-force: anyone's)

A desktop belongs to the agent that ran desk new (the first non-shell
ancestor, or $DESK_OWNER_PID) and closes when that agent exits.

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
	case "help", "-h", "-help", "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	if err != nil {
		log.Fatal(err)
	}
}
