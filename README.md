# demo-kit

Agentic computer use demo kit for Linux: disposable screens for AI agents,
recordings of everything they do, a browser that doesn't look like a bot,
narrated demo videos, and one web page to watch it all.

| Tool       | What it does                                                    |
| ---------- | --------------------------------------------------------------- |
| `desk`     | GPU-accelerated X displays for agents, recorded to mp4; `desk browser` runs Chromium on them for [agent-browser](https://github.com/vercel-labs/agent-browser) |
| `demo`     | narrated videos from a JSON script: recordings, terminals, drawn code and diffs, captions, chapters |
| `deskview` | a web page with a live tab per display, its sound, and your published files |
| `skills/`  | agent skills (Claude Code, Codex) that teach agents to use all of the above |

Status: early. It runs daily on one machine; CI installs it on Debian,
Ubuntu, Fedora and Arch. Issues and PRs welcome.

## Install

```sh
git clone https://github.com/keyclicker/demo-kit
cd demo-kit
./install.sh
```

`install.sh` asks before each step, and is safe to rerun:

1. System packages with `sudo`: TigerVNC, xfwm4, ffmpeg, xdotool, scrot,
   ImageMagick, PipeWire tools, Chromium, Go. Debian, Ubuntu, Fedora,
   RHEL 9, Arch and their derivatives. Fedora and RHEL get ffmpeg from
   RPM Fusion, RHEL also EPEL and CRB, but no scrot (screenshots use
   ImageMagick's `import`); EL 10 lacks packages and is refused. Ubuntu
   gets Chromium from Flathub (no snap).
2. `go build` of `desk`, `demo` and `deskview` into `~/.local/bin`.
3. A starter config, the `deskview` user service, and the skills linked
   into `~/.claude/skills` and `~/.codex/skills`.

Flags: `--yes` (no questions), `--no-deps` (skip packages),
`--only desk,demo` (build a subset).

Needs: Linux with systemd, Go 1.21+ (it fetches the newer toolchain
itself). The host may run X11, Wayland or nothing at all: desk displays
are their own X servers.

## Quick start

```sh
D=$(desk new)                      # a 1280x960 display, recording
DISPLAY=$D setsid -f xterm         # anything X11 runs on it
desk ls                            # displays, owners, recordings
desk rm $D                         # stop; prints the video's path
```

A display belongs to the agent process that made it and goes away with
it. A browser for agents, driven with agent-browser:

```sh
P=$(desk browser start --display $D https://example.com)
agent-browser connect $P
desk browser stop $P
```

Sound: each display has its own sink, `desk-20` for `:20`. Start apps
with `PULSE_SINK=desk-20` and their sound lands in its recording (and
still plays on your output); `desk browser` does it for you.

Watch: open `http://127.0.0.1:8737/` (deskview). Agent displays are
view-only; set `desk.host_vnc` to get your own display as an interactive
tab.

A narrated video:

```sh
# speech: Gemini TTS via OpenRouter (or any OpenAI-compatible server:
# [tts] below); the key once, readable only by you
(umask 077; echo 'OPENROUTER_API_KEY=sk-or-...' >> ~/.config/demo-kit/.env)
demo build path/to/demo.json       # writes demo.mp4 beside the script
demo frames path/to/demo.mp4       # contact sheet, to check it
```

The script format: `demo build -h` and the `demo` skill.

## Config

One file, `~/.config/demo-kit/config.toml`, every key optional.
`desk config` prints the config in effect; `desk config record.dir` one
value. Typos are errors. All keys, with their defaults:

```toml
[desk]
size = "1280x960"                   # desk new's screen
wm = ["xfwm4", "--compositor=off", "--sm-client-disable"]
host_display = ":0"                 # your own display, for desk claim
host_vnc = ""                       # its VNC (e.g. "127.0.0.1:5900"),
                                    # for an interactive deskview tab

[video]                             # desk rec and demo build
render_node = "auto"                # GPU: "auto", a /dev/dri path, ""
encoder = "auto"                    # VAAPI if it works, else x264

[browser]
command = ["chromium"]              # any Chromium; install.sh sets it
main_profile = "~/.config/chromium" # desk browser --profile main

[record]
dir = "~/.local/share/demo-kit/recordings"
audio = true                        # sound too: needs PipeWire with
                                    # pipewire-pulse, else video only

[demo]
dir = "~/.local/share/demo-kit/demos"   # demo workspaces
scratch = "~/.cache/demo-kit/scratch"   # previews, contact sheets
sink = ""                               # demo say's output; "" = default

[tts]                               # narration: OpenAI-compatible speech
url = "https://openrouter.ai/api/v1"    # POST {url}/audio/speech
key_env = "OPENROUTER_API_KEY"          # env var of its key; "" = none
model = "google/gemini-3.8-flash-tts"
voice = "Umbriel"                       # demo voices lists Gemini's
instructions = "Adult male voice. ..."  # delivery direction; "" = none
tempo = 1.2                             # speed-up, pitch kept (0.5-4)

[deskview]
addr = "127.0.0.1:8737"
audio = true
audio_sink = ""                     # "" = the default output
hosts = []                          # names a proxy serves it under

[publish]
root = "~/.local/share/demo-kit"    # deskview serves it at /files/
url = ""                            # its public URL: links in output
```

A local server instead (Kokoro-FastAPI here), no key:

```toml
[tts]
url = "http://127.0.0.1:8880/v1"
key_env = ""
model = "kokoro"
voice = "am_michael"
instructions = ""
```

One run in another voice, the config left alone: `demo build` and
`demo say` take every `[tts]` key as a flag (`-h` lists them). A
script's own `voice` still wins over them.

```sh
demo build --voice af_heart demo.json

# another engine: say which, and its voice
demo build --tts-url https://openrouter.ai/api/v1 \
  --tts-key-env OPENROUTER_API_KEY \
  --model google/gemini-3.8-flash-tts --voice Umbriel --tempo 1.2 \
  demo.json
```

Secrets never go in the config: they come from the environment, or from
`~/.config/demo-kit/.env` beside it (`KEY=VALUE` lines, `chmod 600`;
the environment wins). `.env` values stay inside demo-kit: they never
reach the programs it runs, and term scenes run without the TTS key's
variable even when your shell exports it.

## Reaching deskview from elsewhere

`deskview` listens on loopback only, and has no login: anyone who reaches
it watches your agents, and controls your display if `host_vnc` is set.
Put something with auth in front of it, for example
[Tailscale](https://tailscale.com/kb/1242/tailscale-serve):

```sh
tailscale serve --bg --set-path /desktops http://127.0.0.1:8737
```

or an SSH tunnel: `ssh -L 8737:127.0.0.1:8737 host`. Every URL in the
page is relative, so a path prefix works.

deskview answers only requests for `localhost` or an IP address (any
port), so a web page that rebinds its own name to your loopback can't
read it. A proxy that passes on the public name, in `Host` as
`tailscale serve` does or in `X-Forwarded-Host`, needs that name in
`deskview.hosts`, else every request is a 403 naming the setting. List
each name you open it under (a tailnet's short name is another one):

```toml
[deskview]
hosts = ["box.tailnet-name.ts.net"]
```

An SSH tunnel needs nothing. Then set `publish.url` to the
public URL of `/files/`, and `desk` and `demo` print links to their
videos.

## Headless machines

No monitor, no speakers? Sound still works: add the virtual speaker in
[`examples/pipewire-null-sink.conf`](examples/pipewire-null-sink.conf)
and set `deskview.audio_sink` and `demo.sink` to `desktop-speaker`. Without
PipeWire, everything but sound works.

## Agent skills

`install.sh` links them; or link `skills/*` into your agent's skills
directory yourself.

- `x11-computer-use`: the computer-use toolset as X11 commands on a desk
  display
- `hybrid-browser`: agent-browser for reading and clicking, X11 for the
  rest
- `demo`: when and how to make a demo video, and how to reply with it

## Troubleshooting

- `desk new` fails: `journalctl --user -u desk-20` (the display number)
- No GPU acceleration: `desk config video.render_node`; TigerVNC before
  1.14 has no `-rendernode` and falls back to software GL
- Recordings use x264 on a GPU machine: VAAPI's test encode failed;
  `video.encoder = "vaapi"` shows why
- `desk claim :0` refuses: the host runs Wayland; use `desk new`
- A recording has no sound: `journalctl --user -u desk-rec-20` says
  why; the app needs `PULSE_SINK=desk-20`

## License

[AGPL-3.0-only](LICENSE). Bundled: noVNC's client and pako (MPL-2.0, MIT;
`cmd/deskview/web/novnc`), JetBrains Mono (OFL, `cmd/demo/fonts`).

<!-- SPDX-License-Identifier: AGPL-3.0-only -->
