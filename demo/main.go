// Command demo makes short narrated videos: demos and explainers of
// software (a feature, a change, a codebase, a PR). Speech from
// OpenRouter TTS, code drawn with focus that follows the narration,
// recordings fast-cut to fit it, captions and chapters.
package main

import (
	"fmt"
	"log"
	"os"
)

const usageText = `demo: narrated demo and explainer videos, and speech

  demo build [-o FILE] [--keep] [--draft] [--scene 2,5] script.json
                         render scenes into one mp4: narrated, fast-cut,
                         captioned, with chapters; prints path, URL, cost
  demo frames [--every SECS] VIDEO
                         a contact sheet: frames every few seconds, tiled
  demo code [--lines A-B] [--focus A-B] FILE
  demo code --tree [--depth N] [--lines A-B] [--focus A-B] DIR
  demo code --diff [--split] [--hunks 1,3] [--lines A-B] [--focus A-B] -- [git args]
                         draw code as a code scene would, list its parts
  demo say [-o FILE] [--no-play] [--cost] [text...]
                         speak on :0's speaker (no text: stdin)
  demo voices            the voice names a script can pick

Recordings come from desk (desk new records; desk rec for more).
Script format and workflow: the demo skill. Each command: -h.
`

func main() {
	log.SetFlags(0)
	log.SetPrefix("demo: ")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]

	var err error
	switch cmd {
	case "build":
		err = cmdBuild(args)
	case "code":
		err = cmdCode(args)
	case "frames":
		err = cmdFrames(args)
	case "say":
		err = cmdSay(args)
	case "voices":
		err = cmdVoices(args)
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
