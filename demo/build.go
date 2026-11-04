package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ---- Script ----

// Script is a demo: scenes played in order, each narrated. A scene's
// narration sets its length; its picture is fitted to that.
type Script struct {
	Title  string  `json:"title"`
	Voice  Voice   `json:"voice"`
	Scenes []Scene `json:"scenes"`
}

// Scene is one narrated shot. Exactly one picture source: Clip (a
// recording, optionally From/To seconds), Image (a still; tall ones
// scroll), or Code (a file or diff, drawn).
//
// Narration is Say, or Steps on a code scene: each step's line is
// spoken while its Focus lines are lit and scrolled into view.
type Scene struct {
	Chapter string `json:"chapter,omitempty"` // starts a chapter
	Say     string `json:"say,omitempty"`
	Steps   []Step `json:"steps,omitempty"`

	Clip  string    `json:"clip,omitempty"`
	From  float64   `json:"from,omitempty"`
	To    float64   `json:"to,omitempty"`
	Image string    `json:"image,omitempty"`
	Code  *CodeSpec `json:"code,omitempty"`

	// Secs is the scene's minimum length; needed when nothing is said
	Secs float64 `json:"secs,omitempty"`
}

// Step is one beat of a code scene's narration.
type Step struct {
	Say   string `json:"say"`
	Focus []int  `json:"focus,omitempty"` // lines [from, to] to light up
}

// loadScript reads a script, resolving clips and images against its
// directory. Code paths stay relative to the current directory, like
// git's.
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

	dir := filepath.Dir(path)
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}

	for i := range s.Scenes {
		sc := &s.Scenes[i]
		sc.Clip, sc.Image = resolve(sc.Clip), resolve(sc.Image)
		if err := sc.check(); err != nil {
			return nil, fmt.Errorf("scene %d: %w", i+1, err)
		}
	}
	if len(s.Scenes) == 0 {
		return nil, errors.New("no scenes")
	}

	s.Voice = s.Voice.withDefaults()
	return &s, nil
}

// check rejects scenes with no picture or two, no length, or steps
// where they can't apply.
func (s Scene) check() error {
	sources := 0
	for _, set := range []bool{s.Clip != "", s.Image != "", s.Code != nil} {
		sources += btoi(set)
	}

	switch {
	case sources != 1:
		return errors.New("needs exactly one of clip, image, code")
	case s.Say != "" && s.Steps != nil:
		return errors.New("say or steps, not both")
	case s.Steps != nil && s.Code == nil:
		return errors.New("steps need a code scene")
	case len(s.parts()) == 0 && s.Secs <= 0:
		return errors.New("needs say or steps, or secs for a silent scene")
	case s.To != 0 && s.To <= s.From:
		return errors.New("to must be after from")
	}

	for i, st := range s.Steps {
		if strings.TrimSpace(st.Say) == "" {
			return fmt.Errorf("step %d: needs say", i+1)
		}
		if err := checkRange(fmt.Sprintf("step %d focus", i+1),
			st.Focus); err != nil {
			return err
		}
	}

	if s.Code != nil {
		return s.Code.check()
	}
	return nil
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

// ---- Timing ----

const (
	fps = 30

	captionBand = 128 // px under the 1280x960 picture

	leadIn  = 0.2  // silence before a scene's narration
	stepGap = 0.25 // between steps
	tailOut = 0.4  // after it

	scrollSecs = 0.35 // a focus change scrolls this long

	// maxSpeed warns past it: a clip that needs more is too long for
	// its narration (cut From/To, or say more)
	maxSpeed = 6.0
)

// timing places a scene's spoken steps: Starts[k] is when step k
// begins, Cuts[k] where its picture takes over (Cuts[0] = 0), Length
// the whole scene.
type timing struct {
	Starts, Cuts []float64
	Length       float64
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

// speedFor is the speed-up that fits a clip of length clip into
// length, never a slow-down: a short clip holds its last frame instead.
func speedFor(clip, length float64) float64 {
	return max(1, clip/length)
}

// ---- demo build ----

// cmdBuild renders a script into one narrated, captioned mp4 with
// chapters, in ~/public/demos by default.
func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.String("o", "", "video to write "+
		"(default ~/public/demos/<time>-<title>.mp4)")
	keep := fs.Bool("keep", false, "keep the work dir (scenes, captions)")

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: demo build [flags] script.json\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}

	ctx := context.Background()
	script, err := loadScript(fs.Arg(0))
	if err != nil {
		return err
	}

	video := *out
	if video == "" {
		if video, err = defaultVideoPath(script.Title); err != nil {
			return err
		}
	}
	if video, err = filepath.Abs(video); err != nil {
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

	b := &builder{script: script, work: work}
	if err := b.build(ctx, video); err != nil {
		return err
	}

	b.report(ctx, video)
	return nil
}

// builder renders one script inside its work dir.
type builder struct {
	script *Script
	work   string
	key    string

	speech  [][]spoken // per scene, per step
	timings []timing
	cues    []cue
}

// build runs every stage: speech, scene videos, scene audio, then one
// final encode with captions and chapters.
func (b *builder) build(ctx context.Context, video string) error {
	start := time.Now()

	if err := b.speak(ctx); err != nil {
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
				errs[i] = fmt.Errorf("scene %d: %w", i+1, err)
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

	// Captions get a band of their own under the picture, so they never
	// hide code. Burned on the CPU (libass), then encoded on the GPU.
	// Sizes are in libass units: 288 = the video's height.
	band := fmt.Sprintf("pad=%d:%d:0:0:color=0x0d1117", frameW,
		frameH+captionBand)
	style := "FontName=DejaVu Sans,FontSize=10,PrimaryColour=&H00FFFFFF," +
		"BorderStyle=1,Outline=0,Shadow=0,MarginV=6"

	err := ffmpeg(ctx, b.work, nil,
		"-vaapi_device", renderNode,
		"-f", "concat", "-safe", "0", "-i", "video.txt",
		"-f", "concat", "-safe", "0", "-i", "audio.txt",
		"-f", "ffmetadata", "-i", "chapters.txt",
		"-map", "0:v", "-map", "1:a",
		"-map_metadata", "2", "-map_chapters", "2",
		"-vf", band+",subtitles=captions.srt:force_style='"+style+"',"+
			"format=nv12,hwupload",
		"-c:v", "h264_vaapi", "-qp", "24",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart", video)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "demo: built in %s\n",
		time.Since(start).Round(100*time.Millisecond))
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

	for i, s := range scenes {
		parts := s.parts()
		b.speech[i] = make([]spoken, len(parts))
		durs[i] = make([]float64, len(parts))

		if len(parts) > 0 && b.key == "" {
			key, err := apiKey(ctx)
			if err != nil {
				return err
			}
			b.key = key
		}

		for k, p := range parts {
			wg.Go(func() {
				sp, err := speak(ctx, b.key, b.script.Voice, p.Say)
				if err == nil {
					durs[i][k], err = probeDuration(ctx, sp.Wav)
				}
				b.speech[i][k] = sp

				if err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("scene %d: %w", i+1, err))
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
		t := sceneTiming(durs[i], s.Secs)
		b.timings = append(b.timings, t)

		for k, p := range s.parts() {
			b.cues = append(b.cues,
				narrationCues(p.Say, at+t.Starts[k], durs[i][k])...)
		}
		at += t.Length
	}
	return nil
}

// scenePath names a scene's intermediate file in the work dir.
func (b *builder) scenePath(i int, ext string) string {
	return filepath.Join(b.work, fmt.Sprintf("scene-%02d.%s", i+1, ext))
}

// Every scene video comes out alike, so the concat demuxer can join
// them: 1280x960 (letterboxed), 30 fps, yuv420p BT.709 limited range,
// lossless-ish x264. Sources differ (recordings, PNGs): a change of
// format mid-stream breaks the final GPU encode.
var (
	fitFilter = fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio="+
		"decrease:out_range=tv:out_color_matrix=bt709,"+
		"pad=%d:%d:-1:-1:color=0x0d1117,setsar=1,fps=%d,format=yuv420p,"+
		"setparams=range=tv:colorspace=bt709:color_primaries=bt709:"+
		"color_trc=bt709", frameW, frameH, frameW, frameH, fps)

	sceneCodec = []string{"-an", "-c:v", "libx264", "-preset", "veryfast",
		"-crf", "16"}
)

// sceneVideo renders scene i's picture at exactly its length.
func (b *builder) sceneVideo(ctx context.Context, i int) error {
	s := b.script.Scenes[i]
	length := b.timings[i].Length
	out := b.scenePath(i, "mp4")

	switch {
	case s.Clip != "":
		return b.clipScene(ctx, i, s, length, out)
	case s.Image != "":
		return stillScene(ctx, s.Image, length, out)
	default:
		return b.codeScene(ctx, i, s, out)
	}
}

// clipScene fast-cuts a recording: idle frames go (mpdecimate), then
// it's sped up to fit the narration, or holds its last frame.
func (b *builder) clipScene(ctx context.Context, i int, s Scene,
	length float64, out string) error {

	var cut []string
	if s.From > 0 {
		cut = append(cut, "-ss", ftoa(s.From))
	}
	if s.To > 0 {
		cut = append(cut, "-to", ftoa(s.To))
	}

	// Pass 1: only frames where something changed, back to back
	busy := b.scenePath(i, "busy.mp4")
	args := append(cut, "-i", s.Clip, "-vf",
		fmt.Sprintf("mpdecimate,setpts=N/%d/TB,%s", fps, fitFilter))
	if err := ffmpeg(ctx, "", nil, append(append(args, sceneCodec...),
		busy)...); err != nil {
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
			i+1, speed, busyLen, length)
	}

	// Pass 2: fit to length exactly
	vf := fmt.Sprintf("setpts=PTS/%s,tpad=stop_mode=clone:stop_duration=%s,"+
		"trim=duration=%s,setpts=PTS-STARTPTS,fps=%d",
		ftoa(speed), ftoa(length), ftoa(length), fps)

	return ffmpeg(ctx, "", nil, append([]string{"-i", busy, "-vf", vf},
		append(sceneCodec, out)...)...)
}

// stillScene shows an image for length seconds. One taller than the
// frame scrolls top to bottom, with a short hold at each end.
func stillScene(ctx context.Context, img string, length float64,
	out string) error {

	w, h, err := imageSize(img)
	if err != nil {
		return err
	}

	vf := fitFilter
	if h*frameW > w*frameH {
		hold := min(1.0, length/4)
		vf = fmt.Sprintf("scale=%d:-2,crop=%d:%d:0:"+
			"'(ih-oh)*clip((t-%s)/%s,0,1)',%s",
			frameW, frameW, frameH, ftoa(hold), ftoa(length-2*hold),
			fitFilter)
	}

	return ffmpeg(ctx, "", nil, append([]string{
		"-loop", "1", "-framerate", fmt.Sprint(fps), "-t", ftoa(length),
		"-i", img, "-vf", vf}, append(sceneCodec, out)...)...)
}

// codeScene draws scene i's code. Plain narration: one image, scrolled
// if tall. Steps: one image per step, its focus lit, each scrolling
// from the last step's view to its own.
func (b *builder) codeScene(ctx context.Context, i int, s Scene,
	out string) error {

	l, err := s.Code.load(ctx)
	if err != nil {
		return err
	}

	if s.Steps == nil {
		r, err := drawListing(l, nil)
		if err != nil {
			return err
		}
		png := b.scenePath(i, "png")
		if err := savePNG(r.Img, png); err != nil {
			return err
		}
		return stillScene(ctx, png, b.timings[i].Length, out)
	}

	// ---- One segment per step ----

	t := b.timings[i]
	var segments []string
	prevY := -1

	for k, st := range s.Steps {
		r, err := drawListing(l, st.Focus)
		if err != nil {
			return fmt.Errorf("step %d: %w", k+1, err)
		}

		// No focus: stay where the last step was (the top, first)
		y := prevY
		if st.Focus != nil {
			y = r.focusY(st.Focus[0], st.Focus[1])
		}
		y = max(y, 0)
		if prevY < 0 {
			prevY = y
		}

		png := b.scenePath(i, fmt.Sprintf("step-%02d.png", k+1))
		if err := savePNG(r.Img, png); err != nil {
			return err
		}

		end := t.Length
		if k+1 < len(t.Cuts) {
			end = t.Cuts[k+1]
		}

		seg := b.scenePath(i, fmt.Sprintf("step-%02d.mp4", k+1))
		if err := scrollSegment(ctx, png, prevY, y, end-t.Cuts[k],
			seg); err != nil {
			return err
		}

		segments = append(segments, seg)
		prevY = y
	}

	return joinVideos(ctx, segments, out)
}

// scrollSegment shows a frame-high window of img for length seconds,
// easing its top from y0 to y1 (smoothstep over scrollSecs).
func scrollSegment(ctx context.Context, img string, y0, y1 int,
	length float64, out string) error {

	s := fmt.Sprintf("min(t/%s,1)", ftoa(scrollSecs))
	y := fmt.Sprintf("%d+(%d)*%s*%s*(3-2*%s)", y0, y1-y0, s, s, s)
	vf := fmt.Sprintf("crop=%d:%d:0:'%s',%s", frameW, frameH, y, fitFilter)

	return ffmpeg(ctx, "", nil, append([]string{
		"-loop", "1", "-framerate", fmt.Sprint(fps), "-t", ftoa(length),
		"-i", img, "-vf", vf}, append(sceneCodec, out)...)...)
}

// joinVideos concatenates same-format videos losslessly.
func joinVideos(ctx context.Context, parts []string, out string) error {
	list := out + ".txt"
	if err := os.WriteFile(list, []byte(concatList(parts)), 0o644); err != nil {
		return err
	}

	return ffmpeg(ctx, "", nil, "-f", "concat", "-safe", "0", "-i", list,
		"-c", "copy", out)
}

// imageSize reads an image's dimensions from its header.
func imageSize(path string) (w, h int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", path, err)
	}
	return cfg.Width, cfg.Height, nil
}

// sceneAudio lays scene i's spoken steps at their starts over silence
// of the scene's length.
func (b *builder) sceneAudio(ctx context.Context, i int) error {
	t := b.timings[i]
	length := ftoa(t.Length)
	out := b.scenePath(i, "wav")

	// Silence is the first input: the mix always spans the scene
	args := []string{"-f", "lavfi", "-t", length,
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
	fmt.Fprintf(&graph, "amix=inputs=%d:normalize=0:duration=first[out]",
		len(b.speech[i])+1)

	return ffmpeg(ctx, "", nil, append(args,
		"-filter_complex", graph.String(), "-map", "[out]",
		"-ar", "48000", "-ac", "1", "-c:a", "pcm_s16le", out)...)
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

// report prints the video, its URL, chapters and the TTS cost.
func (b *builder) report(ctx context.Context, video string) {
	fmt.Println(video)
	if u, err := publicURL(ctx, video); err == nil {
		fmt.Println(u)
	}

	chs, length, err := probeChapters(ctx, video)
	if err == nil {
		fmt.Printf("length %s\n", clock(length))
		for _, c := range chs {
			fmt.Printf("  %s %s\n", clock(c.Start), c.Title)
		}
	}

	var ids []string
	for _, scene := range b.speech {
		for _, sp := range scene {
			ids = append(ids, sp.GenID)
		}
	}
	if b.key != "" {
		if usd, err := totalCost(ctx, b.key, ids); err == nil {
			fmt.Printf("tts $%.4f\n", usd)
		}
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

// scratchDir is ~/tmp/demo, demo's working space.
func scratchDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	dir := filepath.Join(home, "tmp", "demo")
	return dir, os.MkdirAll(dir, 0o755)
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// defaultVideoPath is ~/public/demos/<time>-<title>.mp4.
func defaultVideoPath(title string) (string, error) {
	public, err := publicDir()
	if err != nil {
		return "", err
	}

	name := time.Now().Format("2006-01-02-150405")
	slug := slugRE.ReplaceAllString(strings.ToLower(title), "-")
	if slug = strings.Trim(slug[:min(len(slug), 48)], "-"); slug != "" {
		name += "-" + slug
	}

	return filepath.Join(public, "demos", name+".mp4"), nil
}
