package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	flag "github.com/spf13/pflag"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/keyclicker/demo-kit/internal/encode"
)

// ---- Script ----

// Script is a demo: scenes played in order, each narrated. A scene's
// narration sets its length; its picture is fitted to that.
type Script struct {
	Title  string  `json:"title"`
	Voice  Voice   `json:"voice"`
	Scenes []Scene `json:"scenes"`
}

// Scene is one narrated shot with exactly one picture: Clip (a video,
// optionally From/To seconds), or one drawn here: Image, Code, Term or
// Slide.
//
// Narration is Say, or Steps: lines spoken one after another, each
// pointing at part of the picture (code Focus and Marks, an image's
// Box, a Zoom on an image or a clip, a terminal's next command).
type Scene struct {
	Chapter string `json:"chapter,omitempty"` // starts a chapter
	Say     string `json:"say,omitempty"`
	Steps   []Step `json:"steps,omitempty"`
	Voice   *Voice `json:"voice,omitempty"` // over the script's

	Clip     string  `json:"clip,omitempty"`
	From     float64 `json:"from,omitempty"`
	To       float64 `json:"to,omitempty"`
	Audio    bool    `json:"audio,omitempty"`    // keep its sound, ducked
	Decimate *bool   `json:"decimate,omitempty"` // drop idle frames: default
	Trim     *bool   `json:"trim,omitempty"`     // cut still edges: default

	Image string     `json:"image,omitempty"`
	Code  *CodeSpec  `json:"code,omitempty"`
	Term  *TermSpec  `json:"term,omitempty"`
	Slide *SlideSpec `json:"slide,omitempty"`

	// Secs is the scene's minimum length; needed when nothing is said
	Secs float64 `json:"secs,omitempty"`

	num int // its number in the script, for messages
}

// Step is one beat of narration. On code, Focus lines [from, to]
// light up and Marks box text inside them; on an image, Box [x, y, w,
// h] (its pixels) is spotlit; on an image or a clip, Zoom [x, y, w, h]
// (its pixels) fills the frame. Box and Zoom go alone or together.
type Step struct {
	Say   string   `json:"say"`
	Focus []int    `json:"focus,omitempty"`
	Marks []string `json:"marks,omitempty"`
	Box   []int    `json:"box,omitempty"`
	Zoom  []int    `json:"zoom,omitempty"`
}

// loadScript reads a script, resolving clips and images against its
// directory. Code and term paths stay relative to the current
// directory, like git's.
func loadScript(path string) (*Script, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var s Script
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(s.Scenes) == 0 {
		return nil, errors.New("no scenes")
	}

	dir := filepath.Dir(path)
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}

	s.Voice = s.Voice.over(defaultVoice)
	for i := range s.Scenes {
		sc := &s.Scenes[i]
		sc.num = i + 1
		sc.Clip, sc.Image = resolve(sc.Clip), resolve(sc.Image)

		v := s.Voice
		if sc.Voice != nil {
			v = sc.Voice.over(s.Voice)
		}
		sc.Voice = &v

		if err := checkVoice(v); err != nil {
			return nil, fmt.Errorf("scene %d: %w", sc.num, err)
		}
		if err := sc.check(); err != nil {
			return nil, fmt.Errorf("scene %d: %w", sc.num, err)
		}
		if sc.Slide != nil {
			warnGlyphs(os.Stderr, fmt.Sprintf("scene %d", sc.num),
				sc.Slide.texts())
		}
	}
	return &s, nil
}

// check rejects scenes with no picture or two, no length, or options
// and step fields their picture can't take.
func (s Scene) check() error {
	sources := 0
	for _, set := range []bool{s.Clip != "", s.Image != "", s.Code != nil,
		s.Term != nil, s.Slide != nil} {
		sources += btoi(set)
	}

	switch {
	case sources != 1:
		return errors.New("needs exactly one of clip, image, code, term, " +
			"slide")
	case s.Say != "" && s.Steps != nil:
		return errors.New("say or steps, not both")
	case len(s.parts()) == 0 && s.Secs <= 0 && s.Clip == "":
		return errors.New("needs say or steps, or secs for a silent scene")
	case s.To != 0 && s.To <= s.From:
		return errors.New("to must be after from")
	case s.Clip == "" && (s.Audio || s.Decimate != nil || s.Trim != nil ||
		s.From != 0 || s.To != 0):
		return errors.New("from, to, audio, decimate and trim need a clip")
	case s.Audio && s.decimate():
		return errors.New("audio needs every frame: drop decimate")
	}

	for i, st := range s.Steps {
		if err := s.checkStep(st); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}

	switch {
	case s.Code != nil:
		return s.Code.check()
	case s.Term != nil:
		return s.Term.check(len(s.Steps))
	case s.Slide != nil:
		return s.Slide.check()
	}
	return nil
}

// checkStep rejects a step with nothing to say, or pointing where its
// scene's picture can't.
func (s Scene) checkStep(st Step) error {
	switch {
	case strings.TrimSpace(st.Say) == "":
		return errors.New("needs say")
	case (st.Focus != nil || st.Marks != nil) && s.Code == nil:
		return errors.New("focus and marks need a code scene")
	case st.Box != nil && s.Image == "":
		return errors.New("box needs an image scene (a clip: zoom)")
	case st.Zoom != nil && s.Image == "" && s.Clip == "":
		return errors.New("zoom needs an image or a clip scene")
	case slices.Contains(st.Marks, ""):
		return errors.New("empty mark")
	case st.Box != nil && !isRegion(st.Box):
		return errors.New("box: want [x, y, w, h] in the image's pixels")
	case st.Zoom != nil && !isRegion(st.Zoom):
		return errors.New("zoom: want [x, y, w, h] in its pixels")
	}
	return checkRange("focus", st.Focus)
}

// isRegion reports whether r is [x, y, w, h], placed and not empty.
func isRegion(r []int) bool {
	return len(r) == 4 && r[0] >= 0 && r[1] >= 0 && r[2] >= 1 && r[3] >= 1
}

// decimate is whether a clip drops its idle frames: yes unless told,
// keeping its sound, or with nothing said (no narration to fit).
func (s Scene) decimate() bool {
	if s.Decimate != nil {
		return *s.Decimate
	}
	return !s.Audio && len(s.parts()) > 0
}

// trim is whether a clip's still edges go (trimStill): yes unless
// told.
func (s Scene) trim() bool {
	return s.Trim == nil || *s.Trim
}

// minLength is the scene's floor under its narration: secs, or for a
// clip with nothing said, its cut's own length. A narrated clip gets
// no floor: it speeds up to fit what's said, never trails silence.
func (s Scene) minLength(cut span) float64 {
	if s.Secs == 0 && s.Clip != "" && len(s.parts()) == 0 {
		return cut.to - cut.from
	}
	return s.Secs
}

// parts is what the scene says, as steps: Say is a single step.
func (s Scene) parts() []Step {
	if s.Steps != nil {
		return s.Steps
	}
	if strings.TrimSpace(s.Say) != "" {
		return []Step{{Say: s.Say}}
	}
	return nil
}

// scriptHelp is the script format, one line per field: the reference
// agents read (the demo skill points here).
const scriptHelp = `
script.json: one object
  title    string  the video's title (and its first chapter's name)
  voice    object  name (demo voices), instructions, tempo, model;
                   each over config tts, or this build's flags
  scenes   array   played in order, each with one picture:
    clip     path    any video; from, to: seconds to keep. Still edges
                     go (trim: false keeps them), idle frames go
                     (decimate: false keeps them, for real footage),
                     then it speeds up to fit; audio: true keeps its
                     sound, ducked under the voice (and every frame)
    image    path    PNG, JPEG, WebP or GIF (first frame); taller than
                     4:3 pages through
    code     object  file: path, diff: [git diff args], or tree: dir
                     (depth: levels, default 3); then hunks: [n, ...],
                     lines: [from, to], split: true for a diff side by
                     side; align: "top" (default "center": a short one
                     a third down, under its header)
    term     object  run: [commands], typed into a terminal and run for
                     real in one shell (dir: where; default here)
    slide    object  title, callout (one huge line) or end (a closing
                     card); text: a line under it; lines: [a list]
  and narration:
    say      string  spoken over the scene; it sets the scene's length
    steps    array   [{say, ...}]: lines spoken in turn, each pointing:
                     code: focus: [from, to] lights lines, marks: [text]
                     boxes text in them; image (in its pixels): box:
                     [x, y, w, h] spotlights a region; image or clip
                     (in its pixels): zoom: [x, y, w, h] fills the
                     frame with a region, eased in; term: one step per
                     command
    voice    object  this scene's voice, over the script's
    secs     number  minimum length; needed when nothing is said, but
                     a clip then plays its own (trimmed) cut
    chapter  string  starts a chapter here

Clips and images resolve against the script; code and term against
the current directory. The built video is checked for dead air: still
and silent 1.5 s or longer warns, naming its scene.
`

// ---- Timing ----

const (
	fps = 30

	captionBand = 128 // px under the 1280x960 picture

	leadIn  = 0.2  // silence before a scene's narration
	stepGap = 0.25 // between steps
	tailOut = 0.4  // after it

	pageStep = 864 // px a tall still advances per page: 3 lines kept

	// maxSpeed warns past it: a clip that needs more is too long for
	// its narration (cut From/To, or say more)
	maxSpeed = 6.0

	// idleGap is the most a decimated clip waits between changes: a
	// longer gap was idle frames, dropped
	idleGap = 0.1
)

// decimateFilter keeps only the frames where something changed
// (mpdecimate), each after its real gap from the one before, capped at
// idleGap, then brings them to the build's rate. Real gaps, not one
// frame each: a 60 fps clip would play at half speed, a 10 fps one
// triple, whatever rate it is brought to first.
func decimateFilter() string {
	return fmt.Sprintf("mpdecimate,setpts='if(eq(N,0),0,"+
		"PREV_OUTPTS+min(PTS-PREV_INPTS,%s/TB))',fps=%d", ftoa(idleGap), fps)
}

// timing places a scene's spoken steps: step k speaks from Starts[k]
// to Ends[k], its picture takes over at Cuts[k] (Cuts[0] = 0), Length
// is the whole scene.
type timing struct {
	Starts, Ends, Cuts []float64
	Length             float64
}

// sceneTiming lays steps of the given speech lengths out: lead, steps
// with gaps between, tail; at least secs. The length is whole frames
// so audio and video stay in step across scenes; a picture changes
// mid-gap, on a frame.
func sceneTiming(speech []float64, secs float64) timing {
	t := timing{}
	at := leadIn

	for k, d := range speech {
		if k > 0 {
			at += stepGap
		}
		t.Starts = append(t.Starts, at)
		t.Cuts = append(t.Cuts, frames(at-stepGap/2))
		at += d
		t.Ends = append(t.Ends, at)
	}
	if len(t.Cuts) > 0 {
		t.Cuts[0] = 0
	}

	length := secs
	if len(speech) > 0 {
		length = max(at+tailOut, secs)
	}
	t.Length = math.Ceil(length*fps) / fps
	return t
}

// frames rounds seconds to the nearest frame.
func frames(secs float64) float64 {
	return math.Round(secs*fps) / fps
}

// frameCount is how many frames secs is, rounded.
func frameCount(secs float64) int {
	return int(math.Round(secs * fps))
}

// cutFrames is t's cuts, in frames.
func (t timing) cutFrames() []int {
	cuts := make([]int, len(t.Cuts))
	for k, c := range t.Cuts {
		cuts[k] = frameCount(c)
	}
	return cuts
}

// speedFor is the speed-up that fits a clip of length clip into
// length, never a slow-down: a short clip holds its last frame instead.
func speedFor(clip, length float64) float64 {
	return max(1, clip/length)
}

// ---- demo build ----

// cmdBuild renders a script into one narrated, captioned mp4 with
// chapters: demo.mp4 beside the script unless -o. A draft or a few
// scenes make a preview in demo.scratch instead.
func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.StringP("out", "o", "", "video to write (default demo.mp4 "+
		"beside the script; previews: demo.scratch/<demo>-preview.mp4)")
	keep := fs.Bool("keep", false, "keep the work dir (scenes, captions)")
	draft := fs.Bool("draft", false, "preview fast: half size, no captions")
	only := fs.String("scene", "", "preview only these scenes, e.g. 3 or 2,5")
	tts := ttsFlags(fs)

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: demo build [flags] script.json\n")
		fs.PrintDefaults()
		fmt.Fprint(fs.Output(), scriptHelp)
	}
	fs.Parse(args)

	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}

	// Before the script loads: its voices start from config tts
	if err := overrideTTS(*tts); err != nil {
		return err
	}

	ctx, stop := signalContext()
	defer stop()
	script, err := loadScript(fs.Arg(0))
	if err != nil {
		return err
	}

	picks, err := parseList("scene", *only)
	if err != nil {
		return err
	}
	if script.Scenes, err = pickScenes(script.Scenes, picks); err != nil {
		return err
	}
	preview := *draft || picks != nil

	video, err := videoPath(fs.Arg(0), *out, preview)
	if err != nil {
		return err
	}

	scratch, err := scratchDir()
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(scratch, "build-")
	if err != nil {
		return err
	}
	if *keep {
		fmt.Fprintf(os.Stderr, "demo: work dir %s\n", work)
	} else {
		defer os.RemoveAll(work)
	}

	start := time.Now()
	b := newBuilder(script, work, *draft)
	if err := b.build(ctx, video); err != nil {
		return err
	}

	// A preview is a check, not the demo: no transcript beside it
	if !preview {
		if err := b.writeTranscript(video); err != nil {
			return err
		}
	}

	b.report(ctx, video, time.Since(start))
	return nil
}

// pickScenes keeps the scenes numbered in picks (all when nil).
func pickScenes(scenes []Scene, picks []int) ([]Scene, error) {
	if picks == nil {
		return scenes, nil
	}

	var out []Scene
	for _, n := range picks {
		if n > len(scenes) {
			return nil, fmt.Errorf("--scene %d: the script has %d", n,
				len(scenes))
		}
		out = append(out, scenes[n-1])
	}
	return out, nil
}

// videoPath is where the build writes: out if set, else demo.mp4
// beside the script (a demo is a folder: script, result, artifacts),
// or for a preview demo.scratch/<folder>-preview.mp4.
func videoPath(script, out string, preview bool) (string, error) {
	if out == "" && preview {
		dir, err := scratchDir()
		if err != nil {
			return "", err
		}
		abs, err := filepath.Abs(script)
		if err != nil {
			return "", err
		}
		name := filepath.Base(filepath.Dir(abs)) + "-preview.mp4"
		return filepath.Join(dir, name), nil
	}

	if out == "" {
		out = filepath.Join(filepath.Dir(script), "demo.mp4")
	}
	return filepath.Abs(out)
}

// builder renders one script inside its work dir.
type builder struct {
	script *Script
	work   string
	tts    ttsEngine
	draft  bool

	// How scene videos come out (fitFilter, sceneCodec)
	fit   string
	codec []string

	speech   [][]spoken // per scene, per step
	timings  []timing
	cues     []cue
	sessions []session // per scene: a term scene's run (runSessions)
	cuts     []span    // per scene: a clip's cut (cutClips)
}

// newBuilder sets a build up: full size, or for a draft half size and
// a faster, rougher encode.
func newBuilder(s *Script, work string, draft bool) *builder {
	b := &builder{script: s, work: work, draft: draft,
		fit: fitFilter(frameW, frameH), codec: sceneCodec("veryfast", 16)}
	if draft {
		b.fit, b.codec = fitFilter(frameW/2, frameH/2),
			sceneCodec("ultrafast", 23)
	}
	return b
}

// build runs every stage: speech, term sessions, scene videos, scene
// audio, then one final encode with captions and chapters, checked for
// dead air.
func (b *builder) build(ctx context.Context, video string) error {
	if err := b.cutClips(ctx); err != nil {
		return err
	}
	if err := b.speak(ctx); err != nil {
		return err
	}
	if err := b.runSessions(ctx); err != nil {
		return err
	}

	// ---- Scenes, in parallel ----

	n := len(b.script.Scenes)
	errs := make([]error, n)
	limit := make(chan struct{}, 4)
	var wg sync.WaitGroup

	for i := range n {
		wg.Go(func() {
			limit <- struct{}{}
			defer func() { <-limit }()

			err := b.sceneVideo(ctx, i)
			if err == nil {
				err = b.sceneAudio(ctx, i)
			}
			if err != nil {
				errs[i] = fmt.Errorf("scene %d: %w", b.script.Scenes[i].num,
					err)
			}
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return err
	}

	// ---- Assemble ----

	if err := b.writeSidecars(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(video), 0o755); err != nil {
		return err
	}

	// Written aside, then renamed: a failed build keeps the last video
	partial := strings.TrimSuffix(video, ".mp4") + ".partial.mp4"
	defer os.Remove(partial)

	enc, err := encode.Pick(cfg.Video)
	if err != nil {
		return err
	}

	args := append(enc.Device(),
		"-f", "concat", "-safe", "0", "-i", "video.txt",
		"-f", "concat", "-safe", "0", "-i", "audio.txt",
		"-f", "ffmetadata", "-i", "chapters.txt",
		"-map", "0:v", "-map", "1:a",
		"-map_metadata", "2", "-map_chapters", "2")

	if b.draft {
		// The scenes as they are: no band, no captions, no re-encode
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args, "-vf", finalFilter(len(b.cues) > 0)+enc.Upload())
		args = append(args, enc.Codec(24)...)
	}

	args = append(args, "-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart", partial)
	if err := ffmpeg(ctx, b.work, nil, args...); err != nil {
		return err
	}
	if err := os.Rename(partial, video); err != nil {
		return err
	}
	return b.warnDeadAir(ctx, os.Stderr, video)
}

// finalFilter is the final encode's picture: a band of its own under
// the frame for captions, so they never hide code, and with captions
// (none when nothing is said: libass refuses an empty file) burned in
// on the CPU (libass); the caller adds the encoder's upload. Sizes are in
// libass units: 288 = the video's height.
func finalFilter(captions bool) string {
	vf := fmt.Sprintf("pad=%d:%d:0:0:color=0x0d1117", frameW,
		frameH+captionBand)
	if captions {
		style := "FontName=DejaVu Sans,FontSize=10," +
			"PrimaryColour=&H00FFFFFF,BorderStyle=1,Outline=0,Shadow=0," +
			"MarginV=6"
		vf += ",subtitles=captions.srt:force_style='" + style + "'"
	}
	return vf
}

// cutClips settles where each clip is cut: from and to (its end if
// unset), moved into pauses when it keeps its sound (quietCuts), then
// its still edges trimmed (trimCut).
// Before timing: a clip with nothing said lasts as long as its cut.
func (b *builder) cutClips(ctx context.Context) error {
	b.cuts = make([]span, len(b.script.Scenes))
	for i, s := range b.script.Scenes {
		if s.Clip == "" {
			continue
		}

		// Past the end there's nothing to cut: say so, not ffmpeg
		length, err := probeDuration(ctx, s.Clip)
		if err != nil {
			return fmt.Errorf("scene %d: %w", s.num, err)
		}
		if s.From >= length {
			return fmt.Errorf("scene %d: from %.1f s: past the clip's end "+
				"(%.1f s)", s.num, s.From, length)
		}

		c := span{s.From, cmp.Or(s.To, length)}
		if s.Audio {
			if c.from, c.to, err = quietCuts(ctx, s); err != nil {
				return fmt.Errorf("scene %d: %w", s.num, err)
			}
			c.to = cmp.Or(c.to, length)
		}
		if s.trim() {
			if c, err = trimCut(ctx, os.Stderr, s, c); err != nil {
				return fmt.Errorf("scene %d: %w", s.num, err)
			}
		}
		b.cuts[i] = c
	}
	return nil
}

// speak synthesizes every step of every scene in parallel (cached ones
// are free), then times each scene and its captions.
func (b *builder) speak(ctx context.Context) error {
	scenes := b.script.Scenes
	b.speech = make([][]spoken, len(scenes))
	durs := make([][]float64, len(scenes))
	var errs []error
	var mu sync.Mutex
	var wg sync.WaitGroup
	limit := make(chan struct{}, 6) // requests in flight

	// Ready only when a line has to be bought: a cached build works
	// without a key
	b.tts = newEngine(cfg.TTS)
	if uncached(b.tts, scenes) {
		if err := b.tts.ready(); err != nil {
			return err
		}
	}

	for i, s := range scenes {
		parts := s.parts()
		b.speech[i] = make([]spoken, len(parts))
		durs[i] = make([]float64, len(parts))

		for k, p := range parts {
			wg.Go(func() {
				limit <- struct{}{}
				defer func() { <-limit }()

				sp, err := speak(ctx, b.tts, *s.Voice, p.Say)
				if err == nil {
					durs[i][k], err = probeDuration(ctx, sp.Wav)
				}
				b.speech[i][k] = sp

				if err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("scene %d: %w", s.num, err))
					mu.Unlock()
				}
			})
		}
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return err
	}

	at := 0.0
	for i, s := range scenes {
		t := sceneTiming(durs[i], s.minLength(b.cuts[i]))
		b.timings = append(b.timings, t)

		for k, p := range s.parts() {
			b.cues = append(b.cues,
				narrationCues(p.Say, at+t.Starts[k], durs[i][k])...)
		}
		at += t.Length
	}

	if share := slideShare(scenes, b.timings); share > maxSlideShare {
		fmt.Fprintf(os.Stderr, "demo: slides fill %.0f%% of the video: "+
			"while talking, show the real thing (the app, a terminal, "+
			"the code); slides are accents\n", 100*share)
	}
	return nil
}

// uncached reports whether any line scenes say is missing from the TTS
// cache (cachePath) of engine e.
func uncached(e ttsEngine, scenes []Scene) bool {
	for _, s := range scenes {
		for _, p := range s.parts() {
			wav, err := cachePath(e, *s.Voice, p.Say)
			if err != nil {
				return true
			}
			if _, err := os.Stat(wav); err != nil {
				return true
			}
		}
	}
	return false
}

// maxSlideShare is how much of a video slides may fill before build
// warns: past it, it's a slideshow read aloud, not a demo.
const maxSlideShare = 0.4

// slideShare is the part of scenes' length that slides fill: slide
// scenes, and markdown shown as code (a slide too long for one).
func slideShare(scenes []Scene, timings []timing) float64 {
	var slides, total float64
	for i, s := range scenes {
		prose := s.Code != nil && strings.EqualFold(filepath.Ext(s.Code.File),
			".md")
		if s.Slide != nil || prose {
			slides += timings[i].Length
		}
		total += timings[i].Length
	}
	if total == 0 {
		return 0
	}
	return slides / total
}

// scenePath names a scene's intermediate file in the work dir.
func (b *builder) scenePath(i int, ext string) string {
	return filepath.Join(b.work, fmt.Sprintf("scene-%02d.%s", i+1, ext))
}

// fitFilter makes every scene video alike, so the concat demuxer can
// join them: w x h (letterboxed), 30 fps, yuv420p BT.709 limited range.
// Sources differ (recordings, drawings): a change of format mid-stream
// breaks the final GPU encode.
func fitFilter(w, h int) string {
	return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio="+
		"decrease:out_range=tv:out_color_matrix=bt709,"+
		"pad=%d:%d:-1:-1:color=0x0d1117,setsar=1,fps=%d,format=yuv420p,"+
		"setparams=range=tv:colorspace=bt709:color_primaries=bt709:"+
		"color_trc=bt709", w, h, w, h, fps)
}

// sceneCodec is a scene video's encoder: x264, near lossless.
func sceneCodec(preset string, crf int) []string {
	return []string{"-an", "-c:v", "libx264", "-preset", preset,
		"-crf", fmt.Sprint(crf)}
}

// sceneVideo renders scene i's picture at exactly its length.
func (b *builder) sceneVideo(ctx context.Context, i int) error {
	s := b.script.Scenes[i]
	t := b.timings[i]
	out := b.scenePath(i, "mp4")

	if s.Clip != "" {
		return b.clipScene(ctx, i, s, t, out)
	}

	shots, views, err := b.drawnShots(ctx, i, s, t)
	if err != nil {
		return err
	}

	vf := b.fit
	if views != nil {
		n := frameCount(t.Length)
		vf = zoomFilter(views, t.cutFrames(), n) + "," + vf
	}
	return b.drawnVideo(ctx, frameCount(t.Length), shots, vf, out)
}

// drawnShots draws a scene's picture: one, held (paged if tall), or
// one per step, each pointing at what its step says. Views are where
// an image's steps zoom, nil for none.
func (b *builder) drawnShots(ctx context.Context, i int, s Scene,
	t timing) (shots, []rect, error) {

	n, cuts := frameCount(t.Length), t.cutFrames()

	switch {
	case s.Code != nil:
		sh, err := codeShots(ctx, s, n, cuts)
		return sh, nil, err
	case s.Image != "":
		return imageShots(s, n, cuts)
	case s.Term != nil:
		return b.termShots(i, s, n, cuts), nil, nil
	default:
		return still(drawSlide(*s.Slide), nil, n), nil, nil
	}
}

// codeShots draws a code scene. Plain narration: one drawing, paged if
// tall. Steps: one drawing per step, its focus lit and its view on it.
func codeShots(ctx context.Context, s Scene, n int, cuts []int) (shots,
	error) {

	l, err := s.Code.load(ctx)
	if err != nil {
		return nil, err
	}
	warnGlyphs(os.Stderr, fmt.Sprintf("scene %d", s.num), l.texts())

	if s.Steps == nil {
		r, err := drawListing(l, highlight{})
		if err != nil {
			return nil, err
		}
		return still(r.Img, r.Heads, n), nil
	}

	return stepShots(cuts, n, func(k int) (stepView, error) {
		st := s.Steps[k]
		r, err := drawListing(l, highlight{st.Focus, st.Marks})
		if err != nil {
			return stepView{}, err
		}

		// No focus: stay where the last step was
		y := -1
		if st.Focus != nil {
			y = r.focusY(st.Focus[0], st.Focus[1])
		}
		return stepView{r.Img, r.Heads, y}, nil
	}), nil
}

// imageShots shows an image: fitted to the frame, or to its width and
// paged if taller. A step's box is spotlit and its zoom zoomed into,
// the view on them; steps without show the image plain.
func imageShots(s Scene, n int, cuts []int) (shots, []rect, error) {
	p, err := loadPicture(s.Image)
	if err != nil {
		return nil, nil, err
	}

	if s.Steps == nil {
		return still(p.canvas, nil, n), nil, nil
	}

	// Aimed up front, so a bad box fails before any frame is drawn
	aims := make([]aim, len(s.Steps))
	for k, st := range s.Steps {
		if aims[k], err = p.aim(st); err != nil {
			return nil, nil, fmt.Errorf("step %d: %w", k+1, err)
		}
	}

	var views []rect
	if hasZoom(s.Steps) {
		for _, a := range aims {
			views = append(views, a.view)
		}
	}

	return stepShots(cuts, n, func(k int) (stepView, error) {
		return p.point(aims[k]), nil
	}), views, nil
}

// clipScene fast-cuts a recording: idle frames go (mpdecimate, unless
// decimate is off), then it's sped up to fit the narration, or holds
// its last frame, zoomed where steps say. With audio, its sound is cut
// and sped up alike.
func (b *builder) clipScene(ctx context.Context, i int, s Scene, t timing,
	out string) error {

	length := t.Length
	views, err := clipViews(ctx, s)
	if err != nil {
		return err
	}

	c := b.cuts[i]
	var cut []string
	if c.from > 0 {
		cut = append(cut, "-ss", ftoa(c.from))
	}
	cut = append(cut, "-to", ftoa(c.to))

	// Pass 1: only frames where something changed, back to back
	vf := b.fit
	if s.decimate() {
		vf = decimateFilter() + "," + b.fit
	}
	busy, busyWav := b.scenePath(i, "busy.mp4"), b.scenePath(i, "busy.wav")

	args := append(cut, "-i", s.Clip, "-map", "0:v:0", "-vf", vf)
	args = append(append(args, b.codec...), busy)
	if s.Audio {
		args = append(args, "-map", "0:a:0", "-vn", "-ac", "1", "-ar",
			"48000", busyWav)
	}
	if err := ffmpeg(ctx, "", nil, args...); err != nil {
		return err
	}

	busyLen, err := probeDuration(ctx, busy)
	if err != nil {
		return err
	}

	speed := speedFor(busyLen, length)
	if speed > maxSpeed {
		fmt.Fprintf(os.Stderr, "demo: scene %d: %.1fx speed-up to fit "+
			"%.1fs of activity into %.1fs: cut from/to, or say more\n",
			s.num, speed, busyLen, length)
	}

	// Pass 2: fit to length exactly, counted in frames (a duration
	// rounded to milliseconds keeps a frame too many a third of the time)
	vf = fmt.Sprintf("setpts=PTS/%s,tpad=stop_mode=clone:stop_duration=%s,"+
		"fps=%d,trim=end_frame=%d,setpts=PTS-STARTPTS",
		ftoa(speed), ftoa(length), fps, frameCount(length))

	if views != nil {
		vf += "," + zoomFilter(views, t.cutFrames(), frameCount(length))
	}

	err = ffmpeg(ctx, "", nil, append([]string{"-i", busy, "-vf", vf},
		append(b.codec, out)...)...)
	if err != nil || !s.Audio {
		return err
	}

	// Its sound, at the same speed, padded or cut to the scene
	return ffmpeg(ctx, "", nil, "-i", busyWav, "-af",
		fmt.Sprintf("atempo=%s,apad,atrim=end=%s", ftoa(speed),
			ftoa(length)), "-ar", "48000", "-ac", "1",
		b.scenePath(i, "clip.wav"))
}

// sceneAudio lays scene i's spoken steps at their starts over silence
// of the scene's length; a clip's own sound goes under them, ducked
// while the voice speaks (duckVolume).
func (b *builder) sceneAudio(ctx context.Context, i int) error {
	t := b.timings[i]
	out := b.scenePath(i, "wav")

	// Silence is the first input: the mix always spans the scene
	args := []string{"-f", "lavfi", "-t", ftoa(t.Length),
		"-i", "anullsrc=r=48000:cl=mono"}
	var graph strings.Builder

	for k, sp := range b.speech[i] {
		args = append(args, "-i", sp.Wav)
		fmt.Fprintf(&graph, "[%d:a]adelay=%d:all=1[s%d];", k+1,
			int(math.Round(t.Starts[k]*1000)), k)
	}

	graph.WriteString("[0:a]")
	for k := range b.speech[i] {
		fmt.Fprintf(&graph, "[s%d]", k)
	}
	fmt.Fprintf(&graph, "amix=inputs=%d:normalize=0:duration=first",
		len(b.speech[i])+1)

	if b.script.Scenes[i].Audio {
		args = append(args, "-i", b.scenePath(i, "clip.wav"))
		fmt.Fprintf(&graph, "[voice];[%d:a]volume=eval=frame:volume='%s'"+
			"[clip];[voice][clip]amix=inputs=2:normalize=0:duration=first",
			len(b.speech[i])+1, duckVolume(t))
	}
	graph.WriteString("[out]")

	return ffmpeg(ctx, "", nil, append(args,
		"-filter_complex", graph.String(), "-map", "[out]",
		"-ar", "48000", "-ac", "1", "-c:a", "pcm_s16le", out)...)
}

// Ducking: a clip's sound under the voice
const (
	duckLevel  = 0.25 // its volume while the voice speaks
	duckAttack = 0.15 // s it fades down before a line
	duckDecay  = 0.4  // s it takes to come back after one
)

// duckVolume is an ffmpeg volume expression of t: 1, dipping to
// duckLevel around each spoken step, with linear ramps. The steps'
// times are known, so no compressor has to guess them.
func duckVolume(t timing) string {
	expr := "0"
	for k := range t.Starts {
		down := fmt.Sprintf("clip((t-%s)/%s,0,1)*clip((%s-t)/%s,0,1)",
			ftoa(t.Starts[k]-duckAttack), ftoa(duckAttack),
			ftoa(t.Ends[k]+duckDecay), ftoa(duckDecay))
		expr = fmt.Sprintf("max(%s,%s)", expr, down)
	}
	return fmt.Sprintf("1-%s*%s", ftoa(1-duckLevel), expr)
}

// writeSidecars writes the concat lists, captions and chapters the
// final encode reads.
func (b *builder) writeSidecars() error {
	var videos, audios []string
	var lengths []float64
	for i := range b.script.Scenes {
		videos = append(videos, b.scenePath(i, "mp4"))
		audios = append(audios, b.scenePath(i, "wav"))
		lengths = append(lengths, b.timings[i].Length)
	}

	chs := chapters(b.script.Scenes, lengths, b.script.Title)
	files := map[string]string{
		"video.txt":    concatList(videos),
		"audio.txt":    concatList(audios),
		"captions.srt": srt(b.cues),
		"chapters.txt": ffmetadata(b.script.Title, chs),
	}

	for name, data := range files {
		err := os.WriteFile(filepath.Join(b.work, name), []byte(data), 0o644)
		if err != nil {
			return err
		}
	}
	return nil
}

// writeTranscript saves the captions beside video, as
// <name>.transcript.srt: the demo, searchable and skimmable.
func (b *builder) writeTranscript(video string) error {
	path := strings.TrimSuffix(video, ".mp4") + ".transcript.srt"
	return os.WriteFile(path, []byte(srt(b.cues)), 0o644)
}

// report prints the video, its URL, length and chapters, build time
// and the TTS cost: what the reply delivering a demo states.
func (b *builder) report(ctx context.Context, video string,
	took time.Duration) {
	fmt.Println(video)
	if link, ok := cfg.Publish.Link(video); ok {
		fmt.Println(link)
	}

	chs, length, err := probeChapters(ctx, video)
	if err == nil {
		fmt.Printf("length %s\n", clock(length))
		for _, c := range chs {
			fmt.Printf("  %s %s\n", clock(c.Start), c.Title)
		}
	}
	fmt.Printf("built in %s\n", took.Round(100*time.Millisecond))

	var lines []spoken
	for _, scene := range b.speech {
		lines = append(lines, scene...)
	}
	if len(lines) > 0 {
		fmt.Println(billOf(ctx, b.tts, lines))
	}
}

// ---- Helpers ----

// concatList renders an ffmpeg concat demuxer script for files.
func concatList(files []string) string {
	var b strings.Builder
	for _, f := range files {
		quoted := strings.ReplaceAll(f, "'", `'\''`)
		fmt.Fprintf(&b, "file '%s'\n", quoted)
	}
	return b.String()
}

// ftoa formats seconds for ffmpeg arguments.
func ftoa(f float64) string { return fmt.Sprintf("%.3f", f) }

// scratchDir is demo.scratch, made if missing: demo's throwaway output.
func scratchDir() (string, error) {
	return cfg.Demo.Scratch, os.MkdirAll(cfg.Demo.Scratch, 0o755)
}
