---
name: demo
description: Use when the user wants something told as a video: a demo, an explainer, a tutorial, a walkthrough, a review, a catch-up, an answer. A short narrated video, made with `demo` from screen recordings, terminals and drawn code.
---

# Demo videos

A short narrated video instead of text, on whatever the user wants
told: a change, how something works, a tutorial, a catch-up, an
answer to a question.

Show the thing, explain it, point at what matters. Be creative: pick
the shots, order, pace and tone that fit the subject. Nothing here is
a template, except where files go.

Show the real thing while you talk: the app, a terminal, the code, a
diff. A slide is an accent (a hook, one callout, the end card), never
the narration typed out: a video that's mostly slides is a slideshow
read aloud. `demo build` warns when slides fill over 40% of it. Match
the ask, too: "I don't want to read code" means the app and its
output, with code at most a glance.

## Replies

The reply that delivers a demo is the video and a short list, all
from `demo build`'s output; everything else is in the video:

```md
![demo](/home/u/.local/share/demo-kit/demos/demo-kit-desk-rec/demo.mp4)

- [demo-kit-desk-rec/demo.mp4](https://example.host/files/demos/demo-kit-desk-rec/demo.mp4)
- 1:09, built in 10 s
- speech $0.017
- pushed to [PR #12](https://github.com/o/r/pull/12) ([`4c33fe7`](https://github.com/o/r/commit/4c33fe7))
```

Links go inline, on their names: the URL `demo build` prints (only
when `publish.url` is set; else name the path). The PR line only when
the demo shows a change.

After that, chat as usual, in text, unless the user asks for
demos-only for the whole chat. Never post demos on GitHub.

## Where files go

```
$(demo config demo.dir)/<project>-<topic>/    kebab-case: demo-kit-desk-rec
  demo.json     the script
  demo.mp4      the result: demo build writes it here
  demo.transcript.srt   its narration, written beside it
  artifacts/    what it's made of: clips, images, audio, slides
```

`<project>` is the repo (or app) name. `demo.dir` defaults to
`~/.local/share/demo-kit/demos`; with `publish.url` set, `demo build`
prints the video's URL too. Never delete artifacts; rebuilds overwrite
`demo.mp4`.

## Tools

Record anything on a desk display, driven with `x11-computer-use` or
`hybrid-browser`:

```sh
W=$(demo config demo.dir)/demo-kit-desk-rec; mkdir -p $W/artifacts
D=$(desk new --no-rec)
desk rec start -o $W/artifacts/tour.mp4 $D   # act; idle frames drop by themselves
desk rec stop $D
```

A UI tour: record it once, then cut it into a scene per beat with
`from`/`to`, each said over what it shows. Text must read at 1280x960:
zoom the page (Ctrl+plus) or the app's font until body text is big,
and give a step a `zoom` on what it talks about: the picture closes in
on it (a `box` spotlights it instead, or as well).

Commands and their output need no recording: a `term` scene runs
them once and draws the typing. For interactive programs (TUIs,
prompts), record Ghostty with a demo zsh (short prompt, high
contrast, the user's syntax highlighting, no history), its own
instance, no tmux. `S` is this skill's directory, where this file is:

```sh
S=/path/to/skills/demo
DISPLAY=$D ZDOTDIR=$S/zsh setsid -f ghostty \
  --config-default-files=false --config-file=$S/ghostty.conf
```

No Ghostty: `DISPLAY=$D ZDOTDIR=$S/zsh setsid -f xterm -fa Monospace
-fs 14`.

Diagrams (an architecture, a flow, a before/after table) only when
nothing real shows it: an `html` page in `artifacts/`, rendered
headless, 1280 wide (a frame is 960 tall). Write plain HTML: built-in
styles give it the video's look and a few blocks (`.card`, `.flow`
with `.arrow`, `.grid`, `.center`); `demo html -h` lists them. Steps
point with selectors (`"box": "#api"`) and build it up: an element
with `data-from="2"` shows from step 2. Pages count as slides.

```sh
demo html --step 2 --box '#api' $W/artifacts/arch.html  # PNG, its #ids
```

Code is drawn, not filmed. Preview what a scene will show:

```sh
demo code --lines 440-480 --focus 452-462 --mark 'r.resume()' desk/rec.go
demo code --diff -- origin/master...HEAD -- desk/   # numbered hunks
demo code --diff --split --hunks 3 -- origin/master...HEAD -- desk/run.go
demo code --tree --depth 2 desk      # a file tree, numbered for focus
```

Speech: `demo say "a line"` plays it (`--voice NAME` to try one).
Voices: `demo voices` lists the names Gemini takes, with their style;
the default is Umbriel. A second speaker or a quote: a scene's
`"voice": {"name": "Charon"}`. The voice takes direction:
`instructions` is plain English (pace, mood, role), and Gemini acts
on it. A scene's `voice` changes only the
fields it sets: `{"voice": {"instructions": "Slower, serious: this is
the risky part."}}`. Keep the tuned default for most scenes; direct
the few where the tone turns (a warning, a punchline, the end card).
No inline tags like `[laughs]` in the text: unverified.

Edit it all: `demo build $W/demo.json`, run from the repo (code paths,
git and term commands resolve against the current directory). Speech
is cached: rebuild freely. While editing, `--draft` (half size, no
captions) and `--scene 3` preview into `demo.scratch`
(`demo config demo.scratch`).

## Script

```json
{"title": "desk rec", "scenes": [
  {"chapter": "Why", "image": "artifacts/before.png", "steps": [
    {"say": "..."}, {"say": "...", "box": [880, 140, 300, 60]},
    {"say": "...", "box": [880, 140, 300, 60], "zoom": [700, 40, 640, 300]}]},
  {"clip": "artifacts/tour.mp4", "from": 3, "to": 20, "steps": [
    {"say": "..."}, {"say": "...", "zoom": [960, 80, 240, 220]}]},
  {"clip": "artifacts/tour.mp4", "from": 20, "to": 31, "say": "..."},
  {"term": {"run": ["desk new --no-rec", "desk ls"]}, "steps": [
    {"say": "..."}, {"say": "..."}]},
  {"code": {"file": "desk/rec.go", "lines": [440, 480]}, "steps": [
    {"say": "...", "focus": [452, 462], "marks": ["r.resume()"]},
    {"say": "...", "focus": [464, 466]}]},
  {"code": {"diff": ["origin/master...HEAD", "--", "desk/"],
            "hunks": [2]}, "say": "..."},
  {"html": "artifacts/arch.html", "steps": [
    {"say": "..."}, {"say": "...", "box": "#api"},
    {"say": "...", "zoom": "#db"}]},
  {"slide": {"end": "Thanks", "lines": ["PR #12", "next: ..."]}, "say": "..."}
]}
```

Every field, one line each: `demo build -h`. Narration sets each
scene's length; steps point at the picture as they're said (a code
focus, an image's or a page's box, a zoom on those or a clip, a
terminal's next command).
Recordings lose still edges and idle frames, then speed up to fit; a
clip with nothing said plays its trimmed cut (`trim: false` keeps it
whole). The build warns on dead air (still and silent 1.5 s+): fix
it, unless it's on purpose. Code and terminals size themselves to the frame
(`"align": "top"` keeps short code up top; terminals always are).
Mistakes fail with a reason. Cut clips with `from`/`to`, not by hand:
with `audio: true` the cuts move into pauses, so speech never stops
mid-word.

Lists and notes longer than a slide: markdown in `artifacts/`, shown
as `code` with a step per point. It counts as slides.

Before sending, look at every scene: `demo frames $W/demo.mp4` tiles
a frame every few seconds into one PNG.
