// Command agent-chromium runs the agents' browser.
//
// It's Flatpak Chromium launched as a plain browser with a DevTools
// port on localhost. Agents attach with `agent-browser connect PORT`
// and drive it with X input on top. Unlike a browser agent-browser
// launches itself, it carries no automation flags, so sites see
// navigator.webdriver false, and Widevine plays DRM.
//
// Each start gets a fresh temp profile and its own port, so agents
// run in parallel. The user's main profile is there for tasks that
// need their accounts.
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
)

const usageText = `agent-chromium: the agents' browser (Flatpak Chromium)

  agent-chromium start --display :N [--profile P] [URL...]
                          launch it on display :N, print its DevTools port
  agent-chromium status   list running ones; exit 1 if none
  agent-chromium stop PORT
                          close it gracefully

  --profile temp          fresh and empty, deleted on exit (default)
  --profile main          the user's own: their accounts, their settings
  --profile DIR           any other

temp runs in parallel. main and DIR: one browser per profile, and
start refuses while it's open anywhere, the user's Chromium included.
Attach with:  agent-browser connect PORT
On main, stop it before desk rm: losing its display kills it mid-write.
`

func main() {
	log.SetFlags(0)
	log.SetPrefix("agent-chromium: ")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]

	var err error
	switch cmd {
	case "start":
		err = cmdStart(args)
	case "status":
		err = cmdStatus()
	case "stop":
		err = cmdStop(args)
	case "supervise": // internal: start's detached half
		err = cmdSupervise(args)
	case "help", "-h", "-help", "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	if errors.Is(err, errStopped) {
		os.Exit(1)
	}
	if err != nil {
		log.Fatal(err)
	}
}
