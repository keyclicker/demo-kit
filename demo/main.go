// Command demo makes short narrated videos, PR demos above all: speech
// from OpenRouter TTS, diffs drawn as images, recordings fast-cut to
// fit the narration, captions and chapters, linked in the PR.
package main

import (
	"fmt"
	"log"
	"os"
)

const usageText = `demo: narrated videos for PRs, and speech

  demo build [-o FILE] [-keep] script.json
                         render scenes into one mp4: narrated, fast-cut,
                         captioned, with chapters; prints path, URL, cost
  demo diff [-hunks 1,3] [-o FILE] [git diff args]
                         draw a diff as a PNG, list its numbered hunks
  demo publish [-pr N] FILE
                         link the video (and chapters) in the PR body
  demo say [-o FILE] [-no-play] [-cost] [text...]
                         speak on :0's speaker (no text: stdin)

Recordings come from desk (desk new records; desk rec for more).
Script format and workflow: the pr-demo skill. Each command: -h.
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
	case "diff":
		err = cmdDiff(args)
	case "publish":
		err = cmdPublish(args)
	case "say":
		err = cmdSay(args)
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
