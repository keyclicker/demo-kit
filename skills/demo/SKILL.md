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

The reply that delivers a demo is the video, its URL, and its speech
cost (`demo build` prints it), a line at most: everything else is in
the video. After that, chat as usual, in text, unless the user asks
for demos-only for the whole chat. Never post demos on GitHub.

## Where files go

```
~/public/demos/<project>-<topic>/    kebab-case: demo-kit-desk-rec
  demo.json     the script
  demo.mp4      the result: demo build writes it here
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
D=$(desk new -no-rec)
desk rec start -o $W/artifacts/pause.mp4 $D   # act; pause/resume skip dead time
desk rec stop $D
```

A terminal: the user's Ghostty and zsh, as its own instance with a
plain title bar and no tmux:

```sh
DISPLAY=$D setsid -f ghostty \
  --config-file=$HOME/projects/demo-kit/skills/demo/ghostty.conf
```

Code is drawn, not filmed. Preview what a scene will show:

```sh
demo code -lines 440-480 -focus 452-462 -mark 'r.resume()' desk/rec.go
demo code -diff -- origin/master...HEAD -- desk/   # numbered hunks
```

Speech: `demo say "a line"` plays it; voices and tempo in `demo say -h`.

Edit it all: `demo build $W/demo.json` (speech cached, rebuild freely).

## Script

```json
{"title": "desk rec", "scenes": [
  {"chapter": "Why", "image": "artifacts/before.png", "say": "..."},
  {"clip": "artifacts/pause.mp4", "from": 3, "to": 20, "say": "..."},
  {"code": {"file": "desk/rec.go", "lines": [440, 480]}, "steps": [
    {"say": "...", "focus": [452, 462], "marks": ["r.resume()"]},
    {"say": "...", "focus": [464, 466]}]},
  {"code": {"diff": ["origin/master...HEAD", "--", "desk/"],
            "hunks": [2]}, "say": "..."}
]}
```

Every field, one line each: `demo build -h`. Narration sets each
scene's length; recordings lose idle frames and speed up to fit.
Mistakes fail with a reason.

Look at a frame per scene before sending:
`ffmpeg -ss T -i $W/demo.mp4 -frames:v 1 /tmp/f.png`.
