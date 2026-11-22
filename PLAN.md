# Port plan

demo-kit is extracted from a private, single-machine setup: hand-written Go
tools for agent displays, recording, an agents' browser and narrated demo
videos. This plan turns them into a kit that works on anyone's Linux box.

Status: **draft for review**. Nothing is moved yet.

## Goals

- Works on other people's machines: no hardcoded users, hosts, paths or
  network setup.
- Easy install: one `install.sh` that builds from source and installs
  dependencies on Debian/Ubuntu, Fedora/RHEL and Arch, derivatives included.
- One config file. Defaults are close to the original setup, adapted for a
  random user.
- Replaces the original setup (almost) 1:1 by editing the config only, no
  code forks.
- Missing optional pieces (PipeWire, GPU, Chromium) disable features, never
  break the rest.

## Non-goals (for now)

- Tailscale or any other exposure: `deskview` binds to loopback, the user
  runs `tailscale serve` (or a reverse proxy) by hand.
- Wayland as a display backend. `desk` displays are their own Xvnc servers,
  so they work on Wayland machines as is (see [Platforms](#platforms)).
- openSUSE, Alpine, Void, non-systemd distros.
- Speech providers other than OpenRouter.

## What moves

| Original      | Becomes          | Notes                            |
| ------------- | ---------------- | -------------------------------- |
| `demo/`       | `cmd/demo`       | key from env, paths from config  |
| `desk/`       | `cmd/desk`       | runtime dir, encoder fallback    |
| `chromium/`   | `desk browser`   | Flatpak glue dropped             |
| desktops page | `cmd/deskview`   | page, API, VNC proxy, noVNC      |
| `audio/`      | `cmd/deskview`   | optional                         |
| `skills/`     | `skills/`        | generic wording, config paths    |

Stays private: the home page server, the health watchdog, the virtual
monitor EDID, the host's own `:0` VNC server and the null-sink PipeWire
config (documented as an example instead).

## Layout

One Go module, three binaries:

| Binary     | Does                                         | Needs           |
| ---------- | -------------------------------------------- | --------------- |
| `desk`     | displays, recording, `desk browser`          | Xvnc, ffmpeg    |
| `demo`     | `build`, `code`, `frames`, `say`, `voices`   | ffmpeg, the key |
| `deskview` | `/desktops` page, VNC proxy, audio, `/files/` | `desk`          |

```
cmd/desk/            desk CLI: new, ls, rm, rec, claim, release, browser
cmd/demo/            demo CLI: build, code, frames, say, voices
cmd/deskview/        deskview server; web/ holds the page and noVNC
internal/config/     config.toml: schema, defaults, `<bin> config`,
                     publish links
internal/paths/      XDG dirs, "~/" expansion
skills/              agent skills, linked by install.sh
install.sh
```

Each binary's code is its own `package main` under `cmd/`; `internal/` holds
only what more than one binary uses. Every binary has `<bin> config [KEY]`.

Why one module, not three: all binaries share `internal/config` and
`internal/paths`. Separate modules would turn those into a fourth module
plus `go.work` and per-module version tags, for no gain: nobody imports
these as libraries, and Go compiles only what each binary imports.

`deskview` lists displays through `desk ls --json`, the stable contract
between the two, not `desk`'s internals. `install.sh` can build any subset
of the binaries.

## Config

`$XDG_CONFIG_HOME/demo-kit/config.toml`. Every key is optional; these are the
defaults.

```toml
[desk]
size = "1280x960"
wm = ["xfwm4", "--compositor=off", "--sm-client-disable"]
render_node = "auto"        # "auto", a /dev/dri/renderD* path, or "" (CPU)

[record]
dir = "~/.local/share/demo-kit/recordings"
encoder = "auto"            # "auto" (VAAPI if usable, else x264),
                            # "vaapi", "x264"

[demo]
dir = "~/.local/share/demo-kit/demos"
scratch = "~/.cache/demo-kit/scratch"  # previews, drawn code, sheets
voice = "Umbriel"
sink = ""                   # where `demo say` plays; "" = default output

[deskview]
addr = "127.0.0.1:8737"
host_vnc = ""               # e.g. "127.0.0.1:5900": an extra, interactive
                            # tab for the host's own display
audio = true
audio_sink = ""             # sink whose monitor is streamed; "" = default

[publish]
root = "~/.local/share/demo-kit"   # served by deskview at /files/
url = ""                    # public base URL of /files/; "" = print paths
                            # only, no links

[browser]
command = ["chromium"]      # any Chromium-family browser
profile = "temp"            # "temp" (fresh, deleted on stop) or a path
```

Secrets never live in the config: `demo` reads `OPENROUTER_API_KEY` from the
environment.

### Reproducing the original setup

Roughly this, plus the user's own `tailscale serve` routes:

```toml
[record]
dir = "~/public/recordings"

[demo]
dir = "~/public/demos"
scratch = "~/tmp/demo"
sink = "desktop-speaker"

[deskview]
host_vnc = "127.0.0.1:5900"
audio_sink = "desktop-speaker"

[publish]
root = "~/public"
url = "https://<node>/files/"

[browser]
command = ["flatpak", "run", "org.chromium.Chromium"]
```

The Flatpak sandbox also needs one `flatpak override` for
`~/.cache/demo-kit`. That is machine config, not demo-kit's.

## Paths

One rule per kind, no detection:

| Kind                                  | Default                       |
| ------------------------------------- | ----------------------------- |
| Config                                | `$XDG_CONFIG_HOME/demo-kit/`  |
| Videos, demo workspaces, `/files/`    | `$XDG_DATA_HOME/demo-kit/`    |
| Speech cache, Widevine, temp profiles | `$XDG_CACHE_HOME/demo-kit/`   |
| Display tokens, PIDs, rec state       | `$XDG_RUNTIME_DIR/demo-kit/`  |

The runtime dir replaces `/run/desk`, so install needs no tmpfiles rule and
no sudo beyond packages. `desk` and `demo` print the full path of every file
they make, plus its URL when `[publish] url` is set and the file is under
`[publish] root`.

## Components

### desk

Unchanged model: each display is TigerVNC `Xvnc` plus a window manager, run
as a transient systemd user unit, owned by the agent process that made it.

- GPU: `-rendernode` only when one is found; else software rendering.
- Encoder: today `h264_vaapi` only. Add the x264 fallback, so NVIDIA and
  GPU-less machines record.
- `:0` commands (`claim`, `release`, `rec :0`) need an X11 session; on
  Wayland they fail with a clear message.

### deskview

One loopback HTTP server, run by a systemd user unit that `install.sh`
offers to enable.

- `/desktops`: the page. One noVNC tab per desk display (view-only), plus
  `[deskview] host_vnc` (interactive) when set.
- `/api/desktops`: what `desk ls --json` prints.
- `/vnc/<display>`: WebSocket ↔ VNC TCP proxy in Go. Replaces the
  `websockify` package.
- noVNC: its JS client is vendored into `cmd/deskview/web/novnc/` with its
  MPL-2.0 license and embedded with `go:embed`. Replaces the distro `novnc`
  package, whose name and paths differ per distro.
- `/files/`: `[publish] root`, read-only, dot-names hidden.
- `/audio/stream`: raw s16le stereo 48 kHz from `pw-record` on the sink's
  monitor, fanned out to listeners; runs only while someone listens. One
  stream for the whole login session (`:0` and all desk displays mixed).
  No PipeWire: the 🔊 button stays, disabled, with a tooltip saying why.

### desk browser

Replaces `agent-chromium`.

- `desk browser start --display :N [URL…]` launches `[browser] command`
  with `--remote-debugging-port`, a profile dir and no automation flags
  (sites see `navigator.webdriver == false`), prints the DevTools port.
- `stop PORT` closes it through DevTools (`Browser.close`), and tracking is
  by port, not PID: launchers that fork (`flatpak run`) just work. The
  `bwrap` argv parsing goes away.
- Kept: the Widevine CDM cache for temp profiles (best effort: some distro
  Chromium builds ship without Widevine) and the quiet password-manager
  prefs.

### demo

- `OPENROUTER_API_KEY` from the environment; a clear error when unset.
- No PipeWire: `demo say` writes the `.wav` and skips playback. `demo build`
  never needs PipeWire.
- Publish links through `config.Publish.Link` (it was duplicated in `demo`
  and `desk`, each asking `tailscale status`).

### Skills

`demo`, `x11-computer-use`, `hybrid-browser`: generic wording, paths from
the config, no host names. The demo terminal profile (Ghostty, demo zsh)
ships as an optional extra: only recordings of interactive programs need it,
`term` scenes are drawn.

## Install

`./install.sh [--yes] [--no-deps] [--only desk,demo,deskview]`, from a clone:

1. Detect the family from `/etc/os-release` `ID` / `ID_LIKE`.
2. Install packages with sudo. Fedora needs RPM Fusion (`ffmpeg`), RHEL
   also EPEL (`xfwm4`, TigerVNC): asked before enabling.
3. Check Go: 1.21+ fetches the required toolchain by itself
   (`GOTOOLCHAIN=auto`); older (Debian 12) stops with instructions.
4. `go build` the chosen binaries (default: all three) into `~/.local/bin`.
5. Offer: enable the `deskview` user unit, write a default config, link
   skills into `~/.claude/skills` and `~/.codex/skills`.

| Need            | Debian/Ubuntu                | Fedora/RHEL              | Arch        |
| --------------- | ---------------------------- | ------------------------ | ----------- |
| Xvnc            | `tigervnc-standalone-server` | `tigervnc-server`        | `tigervnc`  |
| WM              | `xfwm4`                      | `xfwm4`                  | `xfwm4`     |
| Recording       | `ffmpeg`                     | `ffmpeg` (RPM Fusion)    | `ffmpeg`    |
| Input, shots    | `xdotool scrot imagemagick`  | `xdotool scrot ImageMagick` | `xdotool scrot imagemagick` |
| Audio, optional | `pipewire-bin`               | `pipewire-utils`         | `pipewire`  |
| Browser         | `chromium`; Ubuntu: Flathub  | `chromium`               | `chromium`  |
| Build           | `golang-go git`              | `golang git`             | `go git`    |

Package names get verified in CI containers per family.

Chromium is required: `desk browser` and the browser skills need it, and
later HTML scenes will too. Debian, Fedora and Arch get their native package.
Ubuntu ships only a snap, so it gets Flathub's `org.chromium.Chromium`
(user scope) plus
`flatpak override --user --filesystem=xdg-cache/demo-kit org.chromium.Chromium`
for the profiles. No snap, no Google Chrome. `install.sh` writes what it
installed into the default `[browser] command`.

## Platforms

- Linux with systemd and an X11-capable userland. The host session may be
  X11, Wayland or none (headless): desk displays are independent X servers,
  and nearly every app still speaks X11.
- Wayland only limits the `:0` features above.

## History and privacy

- Import with `git subtree split` per moved dir, keeping history.
- Rewrite private host names, user names and paths out of commits and
  messages (`git filter-repo`), squash noisy runs of fixups.
- Scan the full rewritten history for secrets, hostnames and IPs before the
  first push.

## Order of work

Each step is one PR.

1. This plan.
2. History import, privacy rewrite, squash. Code builds as is.
3. One module, `internal/config`, `internal/paths`, XDG defaults, env key.
4. `desk` portability: runtime dir, render node and encoder fallbacks.
5. `deskview`: page, API, VNC proxy, embedded noVNC, `/files/`, audio.
6. `desk browser`.
7. `install.sh` and CI (build, tests, per-family install check).
8. Skills.
9. Switch the original setup to demo-kit plus its config (private).

## Later

- Audio in recordings: [#1](https://github.com/keyclicker/demo-kit/issues/1).
- HTML scenes for diagrams, rare by design:
  [#3](https://github.com/keyclicker/demo-kit/issues/3).
- Per-display audio.
- openSUSE.
