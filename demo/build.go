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
	"os/exec"
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
// scroll), or Diff (rendered like `demo diff`, scrolled).
type Scene struct {
	Chapter string `json:"chapter,omitempty"` // starts a chapter
	Say     string `json:"say,omitempty"`

	Clip  string    `json:"clip,omitempty"`
	From  float64   `json:"from,omitempty"`
	To    float64   `json:"to,omitempty"`
	Image string    `json:"image,omitempty"`
	Diff  *DiffSpec `json:"diff,omitempty"`

	// Secs is the scene's minimum length; needed when Say is empty
	Secs float64 `json:"secs,omitempty"`
}

// loadScript reads a script, resolving files against its directory.
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

// check rejects scenes with no picture, two pictures, or no length.
func (s Scene) check() error {
	sources := 0
	for _, set := range []bool{s.Clip != "", s.Image != "", s.Diff != nil} {
		if set {
			sources++
		}
	}

	switch {
	case sources != 1:
		return errors.New("needs exactly one of clip, image, diff")
	case strings.TrimSpace(s.Say) == "" && s.Secs <= 0:
		return errors.New("needs say, or secs for a silent scene")
	case s.To != 0 && s.To <= s.From:
		return errors.New("to must be after from")
	}
	return nil
}

// ---- Timing ----

const (
	fps = 30

	leadIn  = 0.2 // silence before a scene's narration
	tailOut = 0.4 // and after it

	// maxSpeed warns past it: a clip that needs more is too long for
	// its narration (cut From/To, or say more)
	maxSpeed = 6.0
)

// sceneLength is how long a scene runs: its narration plus breathing
// room, at least Secs, rounded up to whole frames so audio and video
// stay in step across scenes.
func sceneLength(speech, secs float64) float64 {
	l := speech
	if speech > 0 {
		l = leadIn + speech + tailOut
	}
	l = max(l, secs)

	return math.Ceil(l*fps) / fps
}

// speedFor is the speed-up that fits a clip of length clip into
// length, never a slow-down: a short clip holds its last frame instead.
func speedFor(clip, length float64) float64 {
	return max(1, clip/length)
}

// ---- demo build ----

// cmdBuild renders a script into one narrated, captioned mp4 with
// chapters, in ~/public/pr-demos by default.
func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.String("o", "", "video to write "+
		"(default ~/public/pr-demos/<repo>/<time>-<title>.mp4)")
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
		if video, err = defaultVideoPath(ctx, script.Title); err != nil {
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

	speech  []spoken
	lengths []float64
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

			if err := b.sceneVideo(ctx, i); err != nil {
				errs[i] = fmt.Errorf("scene %d: %w", i+1, err)
				return
			}
			errs[i] = b.sceneAudio(ctx, i)
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

	// Captions are burned on the CPU (libass), then encoded on the GPU
	style := "FontName=DejaVu Sans,FontSize=13,PrimaryColour=&H00FFFFFF," +
		"BorderStyle=3,Outline=6,OutlineColour=&H60000000,Shadow=0," +
		"MarginV=22"

	err := ffmpeg(ctx, b.work, nil,
		"-vaapi_device", renderNode,
		"-f", "concat", "-safe", "0", "-i", "video.txt",
		"-f", "concat", "-safe", "0", "-i", "audio.txt",
		"-f", "ffmetadata", "-i", "chapters.txt",
		"-map", "0:v", "-map", "1:a",
		"-map_metadata", "2", "-map_chapters", "2",
		"-vf", "subtitles=captions.srt:force_style='"+style+"',"+
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

// speak synthesizes every scene's narration in parallel (cached ones
// are free), then fixes each scene's length and captions.
func (b *builder) speak(ctx context.Context) error {
	scenes := b.script.Scenes
	b.speech = make([]spoken, len(scenes))
	b.lengths = make([]float64, len(scenes))
	durs := make([]float64, len(scenes))
	errs := make([]error, len(scenes))

	var wg sync.WaitGroup
	for i, s := range scenes {
		if strings.TrimSpace(s.Say) == "" {
			continue
		}

		if b.key == "" {
			key, err := apiKey(ctx)
			if err != nil {
				return err
			}
			b.key = key
		}

		wg.Go(func() {
			sp, err := speak(ctx, b.key, b.script.Voice, s.Say)
			if err == nil {
				durs[i], err = probeDuration(ctx, sp.Wav)
			}
			b.speech[i], errs[i] = sp, err
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return err
	}

	at := 0.0
	for i, s := range scenes {
		b.lengths[i] = sceneLength(durs[i], s.Secs)
		if durs[i] > 0 {
			b.cues = append(b.cues,
				narrationCues(s.Say, at+leadIn, durs[i])...)
		}
		at += b.lengths[i]
	}
	return nil
}

// scenePath names a scene's intermediate file in the work dir.
func (b *builder) scenePath(i int, ext string) string {
	return filepath.Join(b.work, fmt.Sprintf("scene-%02d.%s", i+1, ext))
}

// Every scene video comes out alike, so the concat demuxer can join
// them: 1280x960 (letterboxed), 30 fps, yuv420p, lossless-ish x264.
var (
	fitFilter = fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio="+
		"decrease,pad=%d:%d:-1:-1:color=0x0d1117,setsar=1,fps=%d,"+
		"format=yuv420p", frameW, frameH, frameW, frameH, fps)

	sceneCodec = []string{"-an", "-c:v", "libx264", "-preset", "veryfast",
		"-crf", "16"}
)

// sceneVideo renders scene i's picture at exactly its length.
func (b *builder) sceneVideo(ctx context.Context, i int) error {
	s := b.script.Scenes[i]
	length := b.lengths[i]
	out := b.scenePath(i, "mp4")

	switch {
	case s.Clip != "":
		return b.clipScene(ctx, i, s, length, out)

	case s.Image != "":
		return stillScene(ctx, s.Image, length, out)

	default:
		png := b.scenePath(i, "png")
		if _, err := diffImage(ctx, *s.Diff, png); err != nil {
			return err
		}
		return stillScene(ctx, png, length, out)
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

// sceneAudio lays scene i's narration over its length, or silence.
func (b *builder) sceneAudio(ctx context.Context, i int) error {
	length := ftoa(b.lengths[i])
	out := b.scenePath(i, "wav")
	format := []string{"-ar", "48000", "-ac", "1", "-c:a", "pcm_s16le"}

	if b.speech[i].Wav == "" {
		return ffmpeg(ctx, "", nil, append([]string{"-f", "lavfi",
			"-i", "anullsrc=r=48000:cl=mono", "-t", length},
			append(format, out)...)...)
	}

	af := fmt.Sprintf("adelay=%d:all=1,apad=whole_dur=%s,atrim=duration=%s",
		int(leadIn*1000), length, length)
	return ffmpeg(ctx, "", nil, append([]string{"-i", b.speech[i].Wav,
		"-af", af}, append(format, out)...)...)
}

// writeSidecars writes the concat lists, captions and chapters the
// final encode reads.
func (b *builder) writeSidecars() error {
	var videos, audios []string
	for i := range b.script.Scenes {
		videos = append(videos, b.scenePath(i, "mp4"))
		audios = append(audios, b.scenePath(i, "wav"))
	}

	chs := chapters(b.script.Scenes, b.lengths, b.script.Title)
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
	for _, sp := range b.speech {
		ids = append(ids, sp.GenID)
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

// defaultVideoPath is ~/public/pr-demos/<repo>/<time>-<title>.mp4,
// <repo> from this checkout's origin.
func defaultVideoPath(ctx context.Context, title string) (string, error) {
	public, err := publicDir()
	if err != nil {
		return "", err
	}

	name := time.Now().Format("2006-01-02-150405")
	slug := slugRE.ReplaceAllString(strings.ToLower(title), "-")
	if slug = strings.Trim(slug[:min(len(slug), 48)], "-"); slug != "" {
		name += "-" + slug
	}

	return filepath.Join(public, "pr-demos", repoName(ctx), name+".mp4"), nil
}

// repoName is origin's repository name, or "misc" outside a repo.
func repoName(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "remote", "get-url",
		"origin").Output()
	if err != nil {
		return "misc"
	}

	url := strings.TrimSuffix(strings.TrimSpace(string(out)), ".git")
	return filepath.Base(strings.ReplaceAll(url, ":", "/"))
}
