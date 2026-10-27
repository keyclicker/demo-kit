// Command agent-chromium runs the agents' shared, logged-in browser.
//
// It's Flatpak Chromium on a persistent profile the user logs into
// once, launched as a plain browser with a DevTools port on localhost.
// Agents attach with `agent-browser connect PORT` and drive it with X
// input on top. Unlike a browser agent-browser launches itself, it
// carries no automation flags, so sites see navigator.webdriver false,
// and the profile keeps logins and the Widevine CDM between runs.
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

  -profile DIR            profile (default ~/.local/share/agent-chromium)

One profile, one browser: start refuses while it runs anywhere.
Attach with:  agent-browser connect 9222
Stop it before desk rm: losing its display kills it mid-write.
Logins and the first Widevine download: the user runs it on :0.
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
