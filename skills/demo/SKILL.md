---
name: demo
description: Use when the user asks for a demo, an explainer, a video walkthrough, or a video review of a change or PR. Records the app on a desk display, draws code with highlights that follow the narration, and edits it all into a short captioned video with `demo`, sent in chat.
---

# Demo videos

A short, fast, narrated video the user watches instead of reading:
a feature in action, how some code works, what a change does. Only
when asked. One tool does the editing: `demo` (`demo -h`).

## 1. Storyboard first

Know the subject (the diff, the code, the feature), then pick 4-8
scenes in the order a viewer needs them. Typical shapes:

- **A change or PR**: why → the result running → the key code → risks
  and tests
- **A feature**: what it's for → using it, start to finish → limits
- **Code explainer**: the big picture → the entry point → the 2-3
  places that matter, in call order

Aim for ~60 s; up to ~2 min for big subjects. Skip boilerplate,
renames, generated code.

Narration: 15-40 spoken words per scene or step, one idea each. Short
sentences. Say identifiers like a person would ("desk rec start",
"the speed-for helper"). Concrete, no filler ("In this scene…",
"Let's take a look"). English.

## 2. Record the running scenes

One clip per scene is easiest: no timestamps to track.

```sh
D=$(desk new -no-rec)                 # set up without recording
W=~/tmp/demo/<topic>; mkdir -p $W
desk rec start -o $W/result.mp4 $D    # ...act...
desk rec stop $D                      # one per scene
desk rm $D
```

Or keep `desk new`'s full recording and cut scenes from it with
`from`/`to` (seconds). `desk rec pause/resume :N` skips dead time.

Drive the app with `hybrid-browser` / `x11-computer-use`, on the desk
display (1280x960):

- **Web**: dev server + `agent-chromium start -display $D URL`
- **Android**: the emulator window on the desk display, or
  `adb shell screenrecord` → a clip
- **TUI/CLI**: `DISPLAY=$D setsid -f xterm -fa Monospace -fs 14`
  (opens maximized), type with `xdotool type` so viewers see the
  command; a plain bash with a short prompt reads best
- **Desktop apps**: launch on `$D`

Act at a normal pace and don't wait for the viewer: idle frames are
cut automatically, then the clip is sped up to fit its narration
(a warning past 6x means: cut tighter, or say more). Do setup before
`rec start`; record only the action.

## 3. Code scenes

Code is drawn, not recorded. Preview with `demo code`, which prints
what it drew (numbered hunks for diffs):

```sh
demo code -lines 440-480 -focus 452-462 desk/rec.go
demo code -diff -- origin/master...HEAD -- desk/     # list hunks
```

Point at what you talk about: give a code scene `steps` instead of
`say`. Each step's line is spoken while its `focus` lines are lit
(the rest fades) and scrolled into view. A step without `focus` keeps
the last view. Focus works on one file per scene.

## 4. Write the script

`$W/demo.json`. Clips and images resolve against it; code paths and
`git diff` run in the current directory:

```json
{
  "title": "desk rec: recordings with pauses",
  "scenes": [
    {"chapter": "Why", "say": "...", "image": "before.png"},
    {"chapter": "Result", "say": "...", "clip": "result.mp4"},
    {"say": "...", "clip": "full.mp4", "from": 31, "to": 48},
    {"chapter": "Code",
     "code": {"file": "desk/rec.go", "lines": [440, 480]},
     "steps": [
       {"say": "Signals drive the recorder...", "focus": [452, 462]},
       {"say": "And when its agent exits...", "focus": [464, 466]}]},
    {"say": "...", "code": {"diff": ["origin/master...HEAD", "--",
       "demo/build.go"], "hunks": [2]}},
    {"chapter": "Tests", "say": "...", "clip": "tests.mp4"}
  ]
}
```

- One picture per scene: `clip` (+ `from`/`to`), `image` (tall ones
  scroll), or `code`: `file` or `diff` (`git diff` args), then
  `hunks` (as `demo code -diff` numbers them) and `lines` [from, to]
- `chapter` starts a chapter; scenes without one continue it
- `secs`: a minimum length; required for a scene with nothing said
- `voice`: `{"voice": "Puck", "tempo": 1.2, "model": "...",
  "instructions": "..."}`, all optional; the defaults are tuned

## 5. Build, check, send

```sh
demo build $W/demo.json     # → path, URL, chapters, TTS cost
```

Check it before the user does: grab a frame per chapter and look
(`ffmpeg -ss T -i V -frames:v 1 /tmp/f.png`). Fix the script and
rebuild: speech is cached, so only changed lines cost time or money.

Send it in chat only: `![demo](/absolute/path.mp4)` (T3 Code plays
it), plus its tailnet URL and the chapter list. Never upload it or
link it on GitHub (PRs, issues, comments).

- Videos land in `~/public/demos` (tailnet only)
- Never delete recordings or demos, including the raw clips
