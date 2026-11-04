---
name: x11-computer-use
description: You SHOULD use it for any computer use on X11 — your own display, screenshots, clicks, typing, keys, scrolling and drags via xdotool, scrot and ImageMagick, recorded to a video.
---

# Computer toolset → X11 cheatsheet

You already know the toolset schema from the inside. This is just the other side:
what each member looks like as a shell command on X11. Use what's helpful.

## Display

Work on your own disposable `desk` display. The user watches it
view-only at `/desktops`; it records itself (`desk rm` saves the video)
and dies with your process.

```sh
desk new                    # → :10 (1280x960, GPU)
desk rm :10                 # when done; prints the video path and URL
```

Too sluggish or broken there? Use the user's `:0`, claimed first:
`desk claim` (exit 3 = user active: ask, then `desk claim -force`),
`desk release` and close your windows when done.

- Prefix every command: `DISPLAY=:10 …` (shell state doesn't persist)
- Start apps detached: `DISPLAY=:10 setsid -f xterm`
- Terminal: Ghostty as the `demo` skill launches it (its own instance,
  a demo zsh, no tmux). Plain `ghostty` may open in the user's Ghostty
  on `:0`: your keys hit their shell
- Browsers: fresh profile (`--user-data-dir=$(mktemp -d)`), else they
  may open in the user's running instance on `:0`

## Record by default

Send the video unless the user opted out:
`![recording](/absolute/path.mp4)` (T3 Code plays it), else its URL
(`~/public/X` is `https://<node>/files/X`).

Never delete recordings, yours included: they're the user's history of
what agents did, never noise. A recording the user won't need: just
don't send it.

`desk new` records until `desk rm`, which saves the video. Long dead
time (installs, builds, waits): `desk rec pause :N`, then `resume`.
Need custom clips (a demo video, the `demo` skill): `desk new -no-rec`,
then `desk rec start [-o FILE] :N` and `desk rec stop :N`.

On `:0`, after `desk claim`: `desk rec start :0` ... `desk rec stop :0`.

## Quick map
| Member | X11 equivalent |
|---|---|
| screenshot | `scrot -o /tmp/s.png` (or `import -window root`) |
| zoom [x0,y0,x1,y1] | `import -window root -crop WxH+x0+y0 png:-` |
| left_click | `xdotool mousemove --sync X Y click 1` |
| right / middle_click | `click 3` / `click 2` |
| double / triple_click | `click --repeat 2 1` / `click --repeat 3 1` |
| left_click_drag | `mousemove X0 Y0 mousedown 1 mousemove X1 Y1 mouseup 1` |
| mouse_move | `xdotool mousemove --sync X Y` |
| left_mouse_down / up | `xdotool mousedown 1` / `mouseup 1` |
| cursor_position | `xdotool getmouselocation --shell` |
| scroll | `click --repeat N B` — buttons 4 up, 5 down, 6 left, 7 right |
| type | `xdotool type --delay 12 -- "$TEXT"` |
| key | `xdotool key --repeat N -- "$TEXT"` — same keysym syntax, passes straight through |
| hold_key | `keydown "$K"; sleep D; keyup "$K"` |
| wait | `sleep D` |

Modifiers on clicks/scroll fit naturally as `keydown ctrl … keyup ctrl` around the action.

## Things worth keeping in mind
- Coordinates you emit live in screenshot space; xdotool works in real screen pixels.
  If screenshots were downscaled, the executor divides by the scale factor (and
  multiplies `getmouselocation` back for cursor_position).
- Long `type` strings tend to go smoother in chunks of a couple hundred chars.
- A short pause after clicks gives the UI a moment to settle before the next block in a batch.
- Stuck modifiers after a failed batch are a classic X11 gotcha — a `keyup` in cleanup helps.
