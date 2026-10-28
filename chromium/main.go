// Command agent-chromium runs the agents' logged-in browser.
//
// It's Flatpak Chromium launched as a plain browser with a DevTools
// port on localhost. Agents attach with `agent-browser connect PORT`
// and drive it with X input on top. Unlike a browser agent-browser
// launches itself, it carries no automation flags, so sites see
// navigator.webdriver false, and profiles keep logins and Widevine.
//
// Two profiles: the agents' own, disposable one (default), and the
// user's main one, for tasks that need their accounts.
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
)

const usageText = `agent-chromium: the agents' logged-in browser (Flatpak Chromium)

  agent-chromium start -display :N [-port 9222] [URL]
                          launch it on display :N, print the DevTools port
  agent-chromium status   print pid, display and port; exit 1 if stopped
  agent-chromium stop     close it gracefully

  -profile agent          the agents' own, disposable profile (default)
  -profile main           the user's own: their accounts, their settings
  -profile DIR            any other

One browser per profile. start refuses while it's open anywhere,
the user's own Chromium included; stop never closes theirs.
Attach with:  agent-browser connect 9222
Stop it before desk rm: losing its display kills it mid-write.
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
		err = cmdStatus(args)
	case "stop":
		err = cmdStop(args)
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
