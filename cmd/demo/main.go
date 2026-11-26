// Command demo makes short narrated videos: demos and explainers of
// software (a feature, a change, a codebase, a PR). Speech from any
// OpenAI-compatible TTS (config tts), code drawn with focus that follows the narration,
// recordings fast-cut to fit it, captions and chapters.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/keyclicker/demo-kit/internal/config"
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
                         speak on demo.sink (no text: stdin)
  demo voices            the voice names a script can pick
  demo config [KEY]      print the config in effect, or one key's value

Recordings come from desk (desk new records; desk rec for more).
Script format and workflow: the demo skill. Each command: -h.
Speech: config [tts], its key from $<tts.key_env>; build and say take
each tts key as a flag, for one run (--voice, --model, --tts-url, ...).
Config: demo config -h.
`

func main() {
	log.SetFlags(0)
	log.SetPrefix("demo: ")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]

	c, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	configure(c)

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
	case "config":
		err = config.Cmd("demo", cfg, args, os.Stdout)
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

// cfg is the config in effect, set once by main.
var cfg config.Config

// configure makes c the config in effect, defaults included.
func configure(c config.Config) {
	cfg = c
	defaultVoice = Voice{
		Model:        c.TTS.Model,
		Name:         c.TTS.Voice,
		Instructions: c.TTS.Instructions,
		Tempo:        c.TTS.Tempo,
	}
}

// signalContext is a command's context: canceled on SIGINT or SIGTERM,
// so what it spawns is killed (CommandContext) and its defers run (work
// dirs go) instead of everything being orphaned. A second signal kills
// as usual once stop is called.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt,
		syscall.SIGTERM)
}
