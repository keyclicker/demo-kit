---
name: demo
description: Use when the user asks for a demo, an explainer, a walkthrough, or a video review of a change or PR. A short narrated video, made with `demo` from screen recordings and drawn code.
---

# Demo videos

A short narrated video instead of text: show the thing, explain it,
point at the code. Be creative: pick the shots, order, pace and tone
that fit the subject. Nothing here is a template, except where files
go.

## Replies

The reply that delivers a demo is the video and a short list, all
from `demo build`'s output; everything else is in the video:

```md
![demo](/home/u/public/demos/demo-kit-desk-rec/demo.mp4)

- [demo-kit-desk-rec/demo.mp4](https://node/files/demos/demo-kit-desk-rec/demo.mp4)
- 1:09, built in 10 s
- speech $0.017
- pushed to [PR #12](https://github.com/o/r/pull/12) ([`4c33fe7`](https://github.com/o/r/commit/4c33fe7))
```

Links go inline, on their names. The PR line only when the demo shows
a change.

After that, chat as usual, in text, unless the user asks for
demos-only for the whole chat. Never post demos on GitHub.

## Where files go

```
~/public/demos/<project>-<topic>/    kebab-case: demo-kit-desk-rec
  demo.json     the script
  demo.mp4      the result: demo build writes it here
  demo.transcript.srt   its narration, written beside it
  artifacts/    what it's made of: clips, images, audio, slides
```

`<project>` is the repo (or app) name. Served at
`https://<node>/files/demos/<project>-<topic>/demo.mp4`. Never delete
artifacts; rebuilds overwrite `demo.mp4`.

## Tools

Record anything on a desk display, driven with `x11-computer-use` or
`hybrid-browser`:

```sh
W=~/public/demos/demo-kit-desk-rec; mkdir -p $W/artifacts
D=$(desk new --no-rec)
desk rec start -o $W/artifacts/pause.mp4 $D   # act; pause/resume skip dead time
desk rec stop $D
```

Commands and their output need no recording: a `term` scene runs
them once and draws the typing. For interactive programs (TUIs,
prompts), record Ghostty with a demo zsh (short prompt, high
contrast, the user's syntax highlighting, no history), its own
instance, no tmux:

```sh
S=$HOME/projects/demo-kit/skills/demo
DISPLAY=$D ZDOTDIR=$S/zsh setsid -f ghostty \
  --config-default-files=false --config-file=$S/ghostty.conf
```

Code is drawn, not filmed. Preview what a scene will show:

```sh
demo code --lines 440-480 --focus 452-462 --mark 'r.resume()' desk/rec.go
demo code --diff -- origin/master...HEAD -- desk/   # numbered hunks
demo code --diff --split --hunks 3 -- origin/master...HEAD -- desk/run.go
demo code --tree --depth 2 desk      # a file tree, numbered for focus
```

Speech: `demo say "a line"` plays it; voices and tempo in `demo say -h`.
The voice takes direction: `instructions` is plain English (pace,
mood, role), and Gemini acts on it. A scene's `voice` changes only the
fields it sets: `{"voice": {"instructions": "Slower, serious: this is
the risky part."}}`. Keep the tuned default for most scenes; direct
the few where the tone turns (a warning, a punchline, the end card).
No inline tags like `[laughs]` in the text: unverified.

Edit it all: `demo build $W/demo.json`, run from the repo (code paths,
git and term commands resolve against the current directory). Speech
is cached: rebuild freely. While editing, `--draft` (half size, no
captions) and `--scene 3` preview into `~/tmp/demo`.

## Script

```json
{"title": "desk rec", "scenes": [
  {"chapter": "Why", "slide": {"title": "desk rec", "text": "..."},
   "say": "..."},
  {"image": "artifacts/before.png", "steps": [
    {"say": "..."}, {"say": "...", "box": [880, 140, 300, 60]},
    {"say": "...", "arrow": [1030, 400]}]},
  {"clip": "artifacts/pause.mp4", "from": 3, "to": 20, "steps": [
    {"say": "..."}, {"say": "...", "zoom": [960, 80, 240, 220]}]},
  {"term": {"run": ["desk new --no-rec", "desk ls"]}, "steps": [
    {"say": "..."}, {"say": "..."}]},
  {"code": {"file": "desk/rec.go", "lines": [440, 480]}, "steps": [
    {"say": "...", "focus": [452, 462], "marks": ["r.resume()"]},
    {"say": "...", "focus": [464, 466]}]},
  {"code": {"diff": ["origin/master...HEAD", "--", "desk/"],
            "hunks": [2]}, "say": "..."},
  {"compare": [{"image": "artifacts/a.png", "label": "before"},
               {"image": "artifacts/b.png", "label": "after"}], "say": "..."},
  {"slide": {"end": "Thanks", "lines": ["PR #12", "next: ..."]}, "say": "..."}
]}
```

Every field, one line each: `demo build -h`. Narration sets each
scene's length; steps point at the picture as they're said (a code
focus, an image's box or arrow, a clip's zoom, a terminal's next
command). Recordings lose idle frames and speed up to fit. Mistakes
fail with a reason.

Lists, notes and anything longer than a slide: write markdown into
`artifacts/` and show it as `code`, a step per point with its focus.

Before sending, look at every scene: `demo frames $W/demo.mp4` tiles
a frame every few seconds into one PNG.
