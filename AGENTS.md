# demo-kit: notes for agents

How the pieces work and why, for whoever changes them. Users read
`README.md`; the port's history is `PLAN.md`.

## Rules

- One Go module; each binary is `package main` in `cmd/<bin>/`;
  `internal/` only for what several binaries share: `config` (the one
  config file), `paths` (XDG dirs), `encode` (H.264 choice)
- A new setting: field + doc in `internal/config` (schema, default,
  `resolve` check), the README's key table. Every binary has
  `<bin> config [KEY]`. Secrets only via env / `.env`, never config,
  and read only through `Config.Secret` (env first, then `.env`):
  `.env` is never exported, so nothing spawned sees it
- Nothing machine-specific in code, docs or commits: no host names,
  user names, home paths or tailnet names. Machine choices go in the
  user's config
- `install.sh` changes: `shellcheck`, then the CI install job (or the
  same containers locally with Docker)
- Branch + PR per change; Conventional Commits

## desk

`desk new [--size WxH] [--no-rec]` (default 1280x960) prints `:N` once
the display is up; `desk ls`, `desk rm [--force] :N... | --all` (prints
the finished videos: path, plus a link when `publish.url` is set);
`desk rec start|pause|resume|stop :N`. Agents learn it from the
`x11-computer-use` skill (`skills/`).

- Desktop `:N` (20-99) = transient user unit `desk-N`, main process
  `desk run --owner PID:START N WxH`:
  - Not 10-19: sshd forwards X from `:10` (`X11DisplayOffset`) into
    the same `~/.Xauthority` entries, and its displays are loopback TCP
    only (6000+N), no socket or lock. `freeDisplay` also skips any N
    whose port is bound. Older desktops on 10-19 still list and `rm`
  - `Xtigervnc` (else `Xvnc`) `:N -rendernode NODE`: GL on the GPU via
    DRI3. NODE is `video.render_node`, passed only when this Xvnc has
    the flag (TigerVNC 1.14+); else software GL. VNC on `127.0.0.1:59NN`, which ignores all VNC input
    (`-Accept*=0`): agents drive it over X, humans watch
  - `desk.wm`, default `xfwm4 --compositor=off`: compositing glitches
    under Xvnc DRI3
  - Listed apps open maximized, once (`maximizeClasses` in
    `maximize.go`, matched on WM_CLASS's class): agents may resize
    after. Dialogs, transients and unlisted apps keep their size.
    Terminals too (Ghostty, xterm): sizing themselves to the screen
    (`-maximized`) pushes xfwm4's frame off its right edge
  - Fresh MIT cookie, added to `~/.Xauthority` so agents' clients
    connect with just `DISPLAY`. Every xauth call goes through `xauth`
    (`run.go`), commands on stdin (`source -`): argv is world-readable
    in `/proc`
  - A silent VNC client of its own, logged in but never requesting
    updates: with no client, Xvnc ticks Present's vblank at 1 Hz
    (`VNCServerST::startFrameClock`), so GL apps and browsers crawl
  - Its own sink, `desk-N` (`audio.go`), unless `record.audio` is
    off: a `pw-loopback` whose playback side goes to the default
    output (passive), so it records apart yet still plays and streams
    as before. Apps reach it with `PULSE_SINK=desk-N`. No PipeWire: no
    sink
- Registry: `$XDG_RUNTIME_DIR/demo-kit` (`stateDir`, 0700): one token
  file per desk display, named by display, in websockify's TokenFile
  format. Each carries a `# desk: view-only` line; `desk ls` drops
  them once their unit is gone. The host display has no file: `desk
  ls` adds `desk.host_display` while its X socket exists, with
  `desk.host_vnc` as its `vnc`
  - `unitAlive` returns an error when `systemctl show` fails (bus
    timeout, manager re-exec): never "dead". Tokens and `.rec-N` then
    stay, with a warning; deskview's `desk ls` every 3 s would
    otherwise wipe every live desktop on one hiccup
- Ownership: the owner is the first non-shell ancestor of `desk new`
  (an agent's tool calls are short-lived shells, so: `claude`,
  `codex`), or `$DESK_OWNER_PID`. Identified by PID + start time
  (`/proc/PID/stat`), stored as a `# owner:` token comment
  - `desk run` polls the owner every 2 s and exits when it's gone
  - `rm` and `rm --all` touch only the caller's desktops; `--force`
    any. Desktops from before ownership show "owner unknown"
- `desk claim [--force] [:0]` / `desk release`: an agent's exclusive
  claim on the host display, `stateDir/.claim-N` (same owner format;
  stale once the owner exits). Without `--force` it needs the user idle
  10+ min (XScreenSaver `MsSinceUserInput`, any input source), else
  exits 3 so the agent asks. Another agent's live claim always blocks.
  `release` stops that display's recording first (`stopRec`, as `rec
  stop`; the recording's owner or `--force`) and prints its video
- `ExecStopPost=desk cleanup N` removes the token, cookie and a dead
  Xvnc's lock and socket, crashes included
- Recording (`rec.go`, `record.go`): transient unit `desk-rec-N`, main
  process `desk rec run`, one code path for `desk new` (unless
  `--no-rec`) and `desk rec start`, any display (`:0` needs the
  caller's claim)
  - ffmpeg x11grab, 30 fps, H.264 by `internal/encode` (VAAPI when
    its test encode works, else x264; picked once per process), to
    `record.dir/<time>-desk-N.mp4` or `-o FILE`. `start`
    returns once ffmpeg reports progress (`-progress pipe:1`)
  - Sound (`audioInput`): ffmpeg's pulse input, AAC; wall-clock
    timestamps as x11grab's, so in sync. A desk display records
    `desk-N.monitor`, the host display the default output's. Video
    only (logged) without `record.audio`, a Pulse server
    (pipewire-pulse) or ffmpeg's pulse input (`pulseCheck`), or the
    sink. Not pw-record into a pipe: its samples carry no clock
  - Segments: pause (`SIGUSR1`) finalizes `FILE.partK.mp4`, resume
    (`SIGUSR2`) starts the next; stop joins them (concat demuxer,
    `-c copy`). `KillMode=mixed`: stop signals only `desk rec run`
  - On a desk display: `BindsTo=` + `After=desk-N`, so `desk rm`
    stops (and finalizes) it before Xvnc goes; `rm` names both units.
    `After=` orders only stop jobs, so when the owner exits, `desk run`
    stops `desk-rec-N` itself before Xvnc
  - Never overwrites: `start` refuses an existing video or `part1`;
    `stop`/`rm` print "not saved" plus the kept parts if the join failed
  - Join (`joinSegments`), all or nothing: every part's packets read
    first (`checkVideo`; the concat demuxer skips a broken part and
    exits 0, and a probe passes one cut short after `+faststart`),
    `-xerror`, into a hidden `.NAME.joining` renamed on success. So the
    video existing means it's whole; parts go only after. A segment
    ffmpeg won't finalize in 20 s is killed, logged
  - State: `stateDir/.rec-N` (owner, video, `recording|paused`),
    written by the recorder only; stops when its owner exits
  - `pause`/`resume`/`stop` need the recording's owner, or `--force`
- `desk new` holds `stateDir/.lock` until the unit is ready (unit is
  `Type=notify`), so parallel calls never share a display
- X authority: `xauthority(display)` is the one answer. A desk
  display's cookie is in `~/.Xauthority`; the host display's in the
  caller's own (`$XAUTHORITY`: GDM, SDDM, Xwayland keep it elsewhere).
  `desk run` sets it for its whole process (xgb and children agree);
  the recorder and the browser supervisor get it too
- Units get the caller's `XDG_*` dirs (`unitEnv`, `--setenv`): the
  user manager may lack them, and the unit must read the same config
- `:0` on Wayland is Xwayland: `claim` and `rec :0` refuse it
  (`xwayland`, the `XWAYLAND` extension)
- After a change: `go test ./...`, `./install.sh --no-deps --yes
  --only desk`. Running desktops keep the old binary until removed

## desk browser

`desk browser start --display :N [--profile P] [URL...]` prints the
DevTools port once it answers; `status` lists running ones (exit 1 if
none); `stop PORT`. Agents attach with `agent-browser connect PORT`
and learn it from the `hybrid-browser` skill.

- `browser.command`, any Chromium, not agent-browser's Chrome for
  Testing: no automation flags (`navigator.webdriver` false). Flatpak
  is just config (`["flatpak", "run", "org.chromium.Chromium"]`)
- Gets `PULSE_SINK=desk-N` when that sink is up: its sound is recorded
- Env (`browserEnv`): the display and its `xauthority`, no
  `WAYLAND_DISPLAY` or `XDG_SESSION_TYPE`: with them a recent Chromium
  opens on the user's Wayland desktop. No flags for it: `browser.command`
  is the user's
- A browser is its DevTools port, never a process found in `/proc`:
  launchers that fork work, and the user's own Chromium can't be hit
- Profiles: `temp` (default): fresh, deleted on exit, any number in
  parallel. `main`: `browser.main_profile`, the user's own. Or a
  directory. A profile with Chromium's `SingletonLock` is refused:
  Chromium would hand the launch to the open browser
- Port: a free one picked by `start` (`freePort`), never
  `--remote-debugging-port=0`: Chromium takes 0 for chromedriver and
  sets `navigator.webdriver`. `start.lock` serializes starts, so two
  never pick the same port or profile
- Runs: `$XDG_CACHE_HOME/demo-kit/browser/run-*/` (on disk: temp
  profiles are big, and a sandbox can be granted it), files `profile`,
  `display`, `port`, `log`, `lock`, and `temp-profile/`. A detached
  `desk browser supervise` runs the browser, waits for its port to
  close, then deletes the run. Both hold `lock`; `start` sweeps runs
  whose lock is free (reboot, kill -9)
  - A launcher that returns (`systemd-run`, `flatpak run`) leaves the
    port to tell: up while it ran, or within 30 s of a clean exit
    (`startWait`), and the run lives until the port closes, uncapped
  - A port that never comes up is a failed launch: the supervisor
    exits, run and log kept. `start` sees the lock go, prints the log's
    tail and removes the run, instead of waiting out its 30 s
- Widevine: Chromium downloads it ~1 min into a profile's first
  launch and loads it only on the next. The newest CDM a temp
  profile downloaded is cached in `$XDG_CACHE_HOME/demo-kit/widevine`
  and copied into each new one (hint file rewritten: absolute path)
- Before each launch on a non-`main` profile, merges into
  `Default/Preferences`: password manager and breach check off. The
  breach bubble grabs input after a login, so clicks "succeed" but do
  nothing. `main` keeps the user's settings
- `stop`: `Browser.close` over the DevTools WebSocket (saves the
  session), then waits for the run to go. `agent-browser close` may
  close the browser too. On `main`, stop before `desk rm`: losing its
  display kills it mid-write
- No `--display` fallback to `$DISPLAY`: agents inherit the user's `:0`

## deskview

One loopback server (`deskview.addr`) for watching displays; routes in
`cmd/deskview/main.go`'s doc comment. Every URL the page uses is
relative, so it works under a path prefix (`tailscale serve
--set-path /desktops/`); the page adds a missing trailing slash itself,
as the proxy strips the prefix and both forms arrive as `/`.

- Page (`web/index.html`): polls `api/desktops` every 3 s: one tab and
  live screen per display with a `vnc` address, each noVNC's `RFB`
  (`scaleViewport`), reconnected 2 s after a drop. Tabs only toggle
  visibility, so switching never reconnects. The host display opens
  interactive; desk displays view-only. A host VNC's password is
  `prompt`ed
- noVNC's client only: `core/` and `vendor/` (pako) in `web/novnc`,
  embedded; version in its `VERSION`. Its UI (`app/`, `vnc.html`)
  stays out: it hid our settings in its sidebar, and its 1.7.0 asks
  before closing an interactive tab. Update: `scripts/update-novnc.sh
  VERSION`; `TestImportsResolve` checks every import is embedded
- 🖱 control / 👁 view only: the shown screen's `rfb.viewOnly`, live.
  Agent tabs: disabled
- Hosts (`hosts.go`, `knownHosts`, every route): only a Host of
  `localhost`, `127.0.0.1`, `[::1]` (any port) or a `deskview.hosts`
  name, else 403 naming the key: a site rebinding its name to loopback
  is same-origin by construction. `tailscale serve` passes the incoming
  Host on (plus it as `X-Forwarded-Host`), so its public name goes in
  `deskview.hosts`. A request header never makes a host known
- `/vnc/{token}`: WebSocket ↔ VNC TCP (`coder/websocket` `NetConn`),
  only to displays `desk ls` lists, same origin only (`Origin` host =
  `Host` or a set, known `X-Forwarded-Host`, exactly; a hostless
  `Origin`, `null` or `file://`, is refused; none is not a browser),
  subprotocol `binary`
- Sound: VNC carries none. `/audio/stream` = raw s16le 48 kHz stereo
  (format in `X-Audio-*` headers), ~1.5 Mbit/s per listener. One
  `pw-record` of `deskview.audio_sink`'s monitor (default: the default
  output), only while someone listens; a listener lagging 160 ms drops
  chunks. One stream per login session: every display plays into it
  (desk displays through their `desk-N` sink's loopback)
- Page side: an AudioWorklet keeps ~80 ms buffered, skips back when
  lag passes 250 ms, reconnects every 1 s. X bells (`RFB`'s `bell`)
  beep through it: only with 🔊 on. No PipeWire or `deskview.audio =
  false` (`audioCheck`): `api/audio` says why, `audio/stream` is a 503
  without a capture, and 🔊 stays, disabled, with that as its tooltip.
  A browser on the
  same machine plays the stream into the default output again: a loop
- Headless boxes: `examples/pipewire-null-sink.conf`, a virtual
  default output, so nothing plays out loud
- `/files/`: `publish.root`, read-only, dot-names hidden in paths and
  listings (`hideDots`). Only directories are wrapped: a wrapped file
  loses `Seek`, and with it Range requests (video seeking)

## demo

`demo build|frames|code|say|voices`: narrated demo and explainer videos (a
feature, a change, a codebase, a PR), and speech. Agents learn it from the
`demo` skill (`skills/`). Videos go to chat, not to GitHub.

The goal: agents demo effortlessly, as if it were a native instrument,
almost like just talking to the user. Universal (any project, any
change), very fast, nearly one shot, and well delivered. Judge every
change to the tool and the skill by that.

- Layout (the skill's one fixed rule): `demo.dir/<project>-<topic>/`
  holds `demo.json`, `demo.mp4`, `demo.transcript.srt` and `artifacts/`
  (clips, images, audio)
- `build script.json`: scenes → one mp4, default `demo.mp4` beside the
  script, its captions beside it as `demo.transcript.srt`. Pictures:
  `clip` (a video) or one drawn in Go: `image`, `code`, `term`,
  `slide`. Narration: `say`, or `steps` that each point at the picture
  (code `focus` and `marks`, an image's `box`, a `zoom` on an image or
  a clip, a term's next command). `voice` per scene, over the script's
  (`Voice.over`). A feature earns its place in videos about other
  things: `compare`, `arrow` and smooth scrolling went, used only in
  demos of `demo`
  - Speech per step, in parallel; a scene is lead 0.2 s + steps with
    0.25 s gaps + tail 0.4 s, in whole frames (`sceneTiming`), so audio
    and video stay in step. Steps mix in at their starts (`amix`)
  - Clips: cut first (`cutClips`, before timing), then `mpdecimate`
    drops idle frames (`decimate: false` keeps them; `decimateFilter`:
    each change after its real gap, capped at one frame of the clip's
    own rate (`probeRate`), so 10 or 60 fps play at their speed and an
    idle stretch shrinks to a frame), then `setpts`
    speeds up to fit (`speedFor`, warns past 6x) or `tpad` holds the
    last frame. With nothing said a clip keeps every frame and lasts
    its (trimmed) cut (no `secs` to probe for). `audio: true` keeps its sound (and
    every frame), `atempo`'d alike and ducked to 25% around each
    spoken step (`duckVolume`: the times are known, so no compressor;
    `sidechaincompress` also ends with its sidechain)
  - Sound-keeping clips are cut in pauses (`quiet.go`): `from`/`to`
    inside speech move to the nearest pause (silencedetect, -35 dB,
    0.25 s+) within 3 s, an end finishing its phrase first, a start
    beginning it; a file whose last 0.1 s is loud (cut mid-speech) is
    cut back to its last pause. A move lands 3.25 s away at most (reach
    plus 0.25 s into the pause), still in it: a long pause's far edge
    would drop the picture between. Moves and misses are printed;
    moves that cross keep the script's own cuts
  - The audio stream's own end counts, not the file's (`listen`): a
    recording's sound stops a few frames before its picture. Past it
    is quiet, unless the sound ends loud; "last 0.1 s" is the audio's
  - Still edges (`still.go`), after the pause moves: a cut starts 0.5 s
    before its first change and ends 0.5 s after its last
    (`trimStill`); with sound kept, its first and last sounds count
    too. Motion is `mpdecimate`'s, as in decimating (8x8 blocks: a
    pointer counts, encoder noise doesn't). `trim: false` keeps the
    cut. Trims of 0.1 s+ are printed
  - Dead air: the built video, previews too, is scanned once more
    (`mpdecimate` + silencedetect, ~1 s per minute); still and silent
    1.5 s+ warns with its times and scene (`warnDeadAir`; `sceneAt`
    counts whole frames, as scene lengths are). Not cut: what's left
    is a `secs` or a bug, and a cut would hide it
  - Slides past 40% of the video warn (`slideShare`): demos show the
    real thing; slides are accents
  - Zoom (`zoom.go`): a step's `zoom` [x, y, w, h] (a clip's pixels
    mapped through the fit, or an image's through its own) grows to
    4:3 around its center (`zoomView`), at most 4x in, and fills the
    frame from the step's cut, eased in over 0.5 s (smoothstep); steps
    without one show it all. Clips and images alike, one filter
    (images: in `drawnVideo`'s pass): ffmpeg's `perspective`
    (`zoomFilter`, `eval=frame`, cubic), each edge one
    expression, the first view's plus each step's change eased, in
    fractions of `W`/`H` (drafts alike). Not `crop`: it sizes itself on
    the first frame, so after `scale=...:eval=frame` it clamps to the
    top-left; not `zoompan`: it crops at whole pixels and jitters
  - Drawn scenes (`draw.go`): a run of `shot`s, each a picture and a
    frame-high view of it, streamed to ffmpeg as raw RGBA frames
    (`drawnVideo`): cuts land on their frame, a tall picture costs no
    more than its view (a full-width RGBA's rows are contiguous: no
    copy). Produced lazily, so one drawing is alive at a time
  - Tall pictures (images, code without steps) jump page by page
    (`still`, 864 px apart). Steps (`stepShots`) cut to a view centered
    on what they point at (`centerY`). The picture changes mid-gap
  - `image`: PNG, JPEG, WebP, GIF (first frame), fitted in Go
    (`loadPicture`, CatmullRom). A step's `box` (source pixels, mapped
    through the fit) is spotlit (`spotlight`: the rest fades as code
    does, outlined, inside where it meets the edge, thinner by the
    step's zoom); `zoom` closes in, alone or with a box. Aimed up front
    (`aim`: numbers only), drawn lazily (`point`). With or without
    steps it's the same picture
  - `term` (`term.go`): commands run once in one bash on a pty
    (`script(1)`, `stty` 102 cols, stdin closed, pagers off), split by
    random per-run markers; output's SGR colors kept (`parseANSI`),
    CRLF a newline, a lone `\r` or `ESC[G` starts the line over, the
    rest dropped, each escape whole per ECMA-48 (`escape`: `ESC ( B`
    in `sgr0`, DCS strings). Scenes' sessions run in script
    order, before any scene is drawn (`runSessions`). Then drawn:
    prompt as the demo zsh's, typing at 30 cps (capped to 40% of its
    step), output at once. One step per command, or one `say` for all.
    Non-zero exits warn. They run on every build. Sized and placed as
    code is, once per scene from its last screen (`screen.fit`), so
    nothing jumps mid-scene
  - `slide` (`slide.go`): `title`, `callout` (110 px, yellow) or `end`,
    plus `text` and `lines`, centered; a slide that doesn't fit fails
    (longer text: markdown as code, which `slideShare` counts too)
  - Scenes → x264 intermediates in `demo.scratch/build-*` (`--keep`),
    joined by the concat demuxer; final pass adds a 128 px caption band
    under the picture (1280x1088: captions never hide code), burns
    captions (SRT pre-wrapped to 2 lines, libass, CPU), encodes H.264
    with `internal/encode` (VAAPI or x264) and adds chapters
    (ffmetadata)
  - Previews, to `demo.scratch/<demo>-preview.mp4`, no transcript:
    `--draft` (640x480, x264 ultrafast, no captions, video copied
    through) and `--scene 2,5` (only those)
- `frames VIDEO`: a contact sheet, a frame every few seconds (about
  20, or `--every`) tiled 4 wide with times and chapters, to
  `demo.scratch/<demo>-frames.png`
- Limits: a drawing is at most 30 frames tall (~1000 lines). ffmpeg
  calls time out at 10 min, term sessions at 2 min, TTS calls at 2 min
  (6 at once, one per distinct line). SIGINT/SIGTERM kill what a
  command spawned (`signalContext`) and remove its work dir
- `code FILE` / `code --diff -- [git diff args]`: draws code as a code
  scene would, as a 1280-wide PNG (`--lines`, `--hunks`, `--focus`,
  `--mark`, before or after FILE), and lists what it drew (numbered
  hunks) first
  - Diffs: `git -c core.quotePath=false diff --src-prefix=a/
    --dst-prefix=b/`, so paths come plain whatever the config (paths
    git still quotes are unquoted, and the tab git ends a `---`/`+++`
    path holding a space with goes first). `context` / `-U`: 3 unless
    set, 0 included
  - Characters JetBrains Mono lacks (emoji, CJK) draw as boxes: code
    and slide scenes warn, naming them (`warnGlyphs`); no font added
  - Overlays (fade, marks) are `color.NRGBA`: `color.RGBA` is
    premultiplied, and a component above its alpha saturates
  - `tree` (`--tree DIR`, `depth`, default 3): a file tree, drawn as
    numbered lines (`tree.go`), so lines, focus, marks and steps work
    as on code. In a repo it lists what git tracks or would (ignored
    files out, `ls-files -z`), as the worktree has it: files deleted
    there left out, each once, a nested repository one directory line;
    else (or in a directory git ignores) every file off
    dot-names, a symlinked DIR walked where it points; directories at
    the depth limit fold into one line with a file count
  - `split` (`--split`): a diff in two 640 px panes, old left, new right
    (`rowsOf`: context on both sides, a run of removals beside the run
    of additions after it), at 15 px so each side keeps ~60 columns.
    Sizes keep whole-pixel advances (JetBrains Mono is 0.6 em), so
    marks land on their columns
  - Lines carry `Pos`, their new-side position (a removed line sits
    after the new line before it): `lines` and `focus` select by it
  - A file's range sits in its file (`surround`): the header reads
    `lines 40-60 of 312` (`shownLines`), the frame around a short one
    shows the lines before and after it, faded and unwrapped, and vi's
    `~` past either end. Neither focus nor marks reach them. The file
    is lexed whole (`lexListing`, once per drawing), so a range opening
    inside a string or comment keeps its colors
  - chroma, `github-dark`; each side of a hunk is lexed whole (new:
    context + added, old: context + removed), so multi-line strings
    and comments keep their color. Embedded JetBrains Mono 2.304
    (`fonts/`, OFL), 20 px, wrapped at ~100 columns
  - Size: the largest of 30, 25, 20 px (`largeMetrics`) that fits one
    frame and wraps no more lines than 20 px does. Most snippets are
    10-30 lines: at a fixed 20 px they filled a quarter of the frame
  - Place (`lift`): the header bar stays on top, flush with the frame's
    edge, a margin under it (`bodyTop`, terminals alike); a short body sits a
    third of the free space down (`align` "center", the default: the
    optical center, dead center looked sunk) or right under it ("top").
    Scrolled (steps, pages), the file's header stays pinned on top,
    margin included, the next file's pushing it up (`pinned`, in
    `view`: a copy of that frame); a focus centers in the frame under it.
    Terminals size alike but always fill from the top, as real ones do:
    lifted, a short session floated in blank space
- `say [text]`: speaks on `demo.sink` (default: the default output)
  with `pw-play`; no PipeWire prints the wav instead; `-o FILE`,
  `--no-play`, `--cost`
- TTS (`tts.go`): engines behind `ttsEngine` (`name`, `ready`,
  `synthesize`: a `Voice` and text in, audio plus its ffmpeg input
  flags out), built from config `tts` (`newEngine`); pricing is the
  optional `ttsPricer`. The one there is, `openaiTTS`: any
  OpenAI-compatible `{tts.url}/audio/speech`, hosted (OpenRouter, the
  default) or local (Kokoro-FastAPI, Speaches). A local engine run as a
  program would be another `ttsEngine`, plus a `tts` key choosing it
  - `response_format: pcm`, the only format
  every provider takes (Gemini TTS refuses mp3). Rate and channels come
  from `Content-Type` (`pcmInput`; no rate: 24 kHz, OpenAI's, warned
  once per run: a server at its model's own rate plays fast or slow);
  ffmpeg decodes to a 48 kHz wav, `atempo` speeds it up (pitch kept)
  - Cache: `$XDG_CACHE_HOME/demo-kit/tts/<sha256(engine, voice,
    text)>.wav`: rebuilds only pay for changed lines. The engine's
    `name` (`openaiTTS`: its URL) is in the key: servers share model
    names (`tts-1`, `alloy`) but not audio, and a receipt is always
    its own engine's. Beside each, a receipt `<same>.json`: its
    generation ID, and its price once looked up
  - A decoded line under 0.05 s (`checkSpeech`: an empty body, a JSON
    error under `audio/pcm`) fails, naming what the server sent; no wav,
    no receipt: a cached empty wav would fail every later build
  - Default voice (`defaultVoice`, from `tts.model`, `tts.voice`,
    `tts.instructions`, `tts.tempo`): `google/gemini-3.8-flash-tts`,
    `Umbriel`, brisk male instructions, tempo 1.2 (~18 chars/s); second
    choice `Puck`. Flash, not lite: lite is cheaper, these are listened
    to. Script field `voice`: `name`, `instructions`, `tempo`, `model`
  - Flags (`ttsFlags`, on `build` and `say`): one per config `tts` key
    (`--tts-url`, `--tts-key-env`, `--model`, `--voice`,
    `--instructions`, `--tempo`), each defaulting to the config's
    value. `overrideTTS` checks the result as the file's is
    (`TTS.Check`) and makes it the config in effect (`configure`), so
    the engine and `defaultVoice` follow: one run in another voice or
    on another engine, the file untouched. A script's own `voice`
    still goes over them. Named flags, not a generic `--set
    KEY=VALUE`: speech is what gets a one-off, and `-h` shows each
    with the config's value
  - Voices (`voices.go`): no voices API, so `geminiVoices` lists the 30
    Gemini names (all tried on flash) with Google's style; `demo
    voices` prints them. A Gemini model with another name fails at
    `checkVoice` (the API's answer: a bare 400). Other models' names
    pass through (Kokoro `am_michael`, Grok `Rex`, ElevenLabs `Liam`)
  - Key: the variable `tts.key_env` names (`OPENROUTER_API_KEY`), from
    the environment or the `.env` beside the config (`cfg.Secret`);
    `""` sends none. A build asks (`ready`) only when a line isn't cached.
    Term scenes run without that variable (`termEnv`): `env` in one
    would draw the key into a shared video
  - Cost: OpenRouter's `GET /generation?id=<X-Generation-Id>`, only
    when a response carries that header (else unpriced); 404 until
    priced, any other error ends it. Priced a few seconds late, so a
    line made in this run polls for up to 30 s; a cached unpriced one
    asks once, quietly (another account's key, an expired generation
    would cost every build 30 s). ~$0.0017 per 7 s line. Kept in the
    receipt. Build and `say --cost` print new spend and the cached
    share (`billOf`): `tts $0.0042 new (3 lines), 9 cached ($0.0150)`,
    plus `N unpriced` (lines cached before receipts, failed lookups;
    a new line's warns)
- Needs `ffmpeg` (libass, libx264), `ffprobe`, `git`, the DejaVu Sans
  font (captions); `pw-play` optional. After a change: `go test
  ./...`, `./install.sh --no-deps --yes --only demo`

