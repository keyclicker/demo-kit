package main

import (
	"errors"
	"fmt"
	flag "github.com/spf13/pflag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A recording is the transient user unit desk-rec-N, main process
// `desk rec run`, for any display: a desk one (started by `desk new`,
// bound to desk-N) or the user's :0. It records in segments, joined
// into one mp4 on stop:
//
//	SIGUSR1 pause: finalize the current segment
//	SIGUSR2 resume: start the next one
//	SIGTERM stop: finalize, join, exit (also when its owner exits)
//
// Its state lives in stateDir/.rec-N, written by the recorder only.

const (
	recPause  = syscall.SIGUSR1
	recResume = syscall.SIGUSR2

	videoPrefix = "# video: "
	statePrefix = "# state: "
)

// Recording is a running desk-rec-N, as `desk ls --json` prints it.
type Recording struct {
	Video  string `json:"video"`
	Paused bool   `json:"paused"`
	Owner  Owner  `json:"owner"`
}

func recUnitName(n int) string { return fmt.Sprintf("desk-rec-%d", n) }

// recPath is display n's recording state. A dotfile, so `desk ls`
// skips it; websockify reads it but finds only comments.
func recPath(n int) string {
	return filepath.Join(stateDir, fmt.Sprintf(".rec-%d", n))
}

// readRec returns display n's live recording, or nil. State left by a
// dead unit is stale: remove it. When systemd can't be asked, the
// state file's word stands.
func readRec(n int) *Recording {
	data, err := os.ReadFile(recPath(n))
	if err != nil {
		return nil
	}

	switch alive, err := unitAlive(recUnitName(n)); {
	case err != nil:
		fmt.Fprintf(os.Stderr, "desk: :%d recording: %v (kept)\n", n, err)
	case !alive:
		os.Remove(recPath(n))
		return nil
	}

	rec, ok := parseRec(string(data))
	if !ok {
		return nil
	}
	return &rec
}

// state names what the recorder is doing: "recording" or "paused".
func (r Recording) state() string {
	if r.Paused {
		return "paused"
	}
	return "recording"
}

// recFile renders a recording's state file.
func recFile(r Recording) string {
	return fmt.Sprintf("%s\n%s%s\n%s%s\n",
		ownerLine(r.Owner), videoPrefix, r.Video, statePrefix, r.state())
}

// parseRec decodes recFile's output; ok is false if a field is missing.
func parseRec(data string) (r Recording, ok bool) {
	var hasOwner, hasVideo bool

	for _, line := range strings.Split(data, "\n") {
		if o, found := parseOwnerLine(line); found {
			r.Owner, hasOwner = o, true
		}
		if v, found := strings.CutPrefix(line, videoPrefix); found {
			r.Video, hasVideo = v, true
		}
		if s, found := strings.CutPrefix(line, statePrefix); found {
			r.Paused = s == "paused"
		}
	}

	return r, hasOwner && hasVideo
}

// writeRec replaces display n's state file atomically.
func writeRec(n int, r Recording) error {
	tmp := recPath(n) + ".tmp"
	if err := os.WriteFile(tmp, []byte(recFile(r)), 0o644); err != nil {
		return err
	}

	return os.Rename(tmp, recPath(n))
}

// parseRecDisplay accepts a desk display or a user one (:0).
func parseRecDisplay(s string) (int, error) {
	if n, err := parseDisplay(s); err == nil {
		return n, nil
	}

	return parseUserDisplay(s)
}

// printRecording reports a finished video: path, and its tailnet URL;
// or, when the join failed, the parts left behind.
func printRecording(n int, video string) {
	if !exists(video) {
		parts, _ := filepath.Glob(strings.TrimSuffix(video, ".mp4") +
			".part*.mp4")
		fmt.Printf(":%d recording not saved: %s (parts kept: %s; see "+
			"journalctl --user -u %s)\n", n, video,
			strings.Join(parts, " "), recUnitName(n))
		return
	}

	fmt.Printf(":%d recording: %s\n", n, video)
	if link, ok := cfg.Publish.Link(video); ok {
		fmt.Printf(":%d url: %s\n", n, link)
	}
}

// ---- Commands ----

const recUsage = `usage:
  desk rec start [-o FILE] [--force] :N  record (default: in record.dir)
  desk rec pause [--force] :N            stop grabbing, keep the video open
  desk rec resume [--force] :N           grab again, into the same video
  desk rec stop [--force] :N             finish: join, print path and URL

desk new records by itself (desk new --no-rec doesn't); desk rm saves it.
:0 needs your desk claim. --force: someone else's desktop or recording.
`

// cmdRec dispatches `desk rec SUBCOMMAND`.
func cmdRec(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, recUsage)
		os.Exit(2)
	}

	sub, args := args[0], args[1:]

	switch sub {
	case "start":
		return cmdRecStart(args)
	case "pause":
		return cmdRecSignal(args, recPause, true)
	case "resume":
		return cmdRecSignal(args, recResume, false)
	case "stop":
		return cmdRecStop(args)
	case "run":
		// Internal: a desk-rec-N unit's main process
		return cmdRecRun(args)
	case "cleanup":
		// Internal: its ExecStopPost
		return cmdRecCleanup(args)
	case "help", "-h", "-help", "--help":
		fmt.Print(recUsage)
		return nil
	}

	fmt.Fprint(os.Stderr, recUsage)
	os.Exit(2)
	return nil
}

// cmdRecStart starts recording a display the caller may use: its own
// desk display, or a user display it claimed.
func cmdRecStart(args []string) error {
	fs := flag.NewFlagSet("rec start", flag.ExitOnError)
	out := fs.StringP("out", "o", "", "video file (default: in record.dir)")
	force := fs.Bool("force", false, "record another agent's display")
	fs.Parse(args)

	if fs.NArg() != 1 {
		return errors.New("usage: desk rec start [-o FILE] :N")
	}

	n, err := parseRecDisplay(fs.Arg(0))
	if err != nil {
		return err
	}

	me, err := callerOwner()
	if err != nil {
		return err
	}

	if err := checkRecordable(n, me, *force); err != nil {
		return err
	}

	video := *out
	if video == "" {
		video = recordingPath(n)
	}
	if video, err = filepath.Abs(video); err != nil {
		return err
	}

	unlock, err := lockState()
	if err != nil {
		return err
	}
	defer unlock()

	if err := startRec(n, me, video); err != nil {
		return err
	}

	fmt.Printf(":%d recording: %s\n", n, video)
	return nil
}

// checkRecordable allows a desk display the caller owns, or a user
// display it claimed; --force skips the owner check.
func checkRecordable(n int, me Owner, force bool) error {
	if n < firstDisplay {
		if o := readClaim(n); o == nil || !o.Same(me) {
			return fmt.Errorf(":%d: `desk claim` it first", n)
		}
		return nil
	}

	desks, err := list()
	if err != nil {
		return err
	}

	for _, d := range desks {
		if d.Token != strconv.Itoa(n) {
			continue
		}
		if !force && (d.Owner == nil || !d.Owner.Same(me)) {
			return fmt.Errorf(":%d isn't yours; --force to record it "+
				"anyway", n)
		}
		return nil
	}

	return fmt.Errorf(":%d: no such desktop", n)
}

// startRec runs desk-rec-N for owner into video, and returns once the
// first frames are encoded. Callers hold the state lock.
func startRec(n int, owner Owner, video string) error {
	unit := recUnitName(n)
	if alive, err := unitAlive(unit); err != nil {
		return err
	} else if alive {
		return fmt.Errorf(":%d is already recording (desk rec stop :%d)",
			n, n)
	}

	// Never record over a video, or over a crashed run's parts
	for _, p := range []string{video, partPath(video, 1)} {
		if exists(p) {
			return fmt.Errorf("%s exists: record to another file", p)
		}
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// TimeoutStopSec: finalizing + joining a long recording takes time.
	// KillMode=mixed: stop signals only `desk rec run`, which SIGINTs
	// its ffmpeg.
	run := []string{"--user", "--quiet", "--collect",
		"--unit=" + unit, fmt.Sprintf("--description=desk rec :%d", n),
		"--property=Type=notify", "--property=TimeoutStartSec=30",
		"--property=TimeoutStopSec=120", "--property=KillMode=mixed",
		fmt.Sprintf("--property=ExecStopPost=%s rec cleanup %d", self, n),
		"--setenv=XAUTHORITY=" + filepath.Join(home, ".Xauthority"),
	}

	// A desk display's recording lives and dies with it, and stops
	// first (After=), so the video is final before Xvnc goes
	if n >= firstDisplay {
		desk := unitName(n) + ".service"
		run = append(run, "--property=BindsTo="+desk,
			"--property=After="+desk)
	}

	run = append(run, unitEnv()...)
	run = append(run, self, "rec", "run",
		fmt.Sprintf("--owner=%d:%d", owner.PID, owner.Start),
		"--out="+video, strconv.Itoa(n))

	start := exec.Command("systemd-run", run...)
	start.Stderr = os.Stderr

	if err := start.Run(); err != nil {
		return fmt.Errorf("start %s: %w (see journalctl --user -u %s)",
			unit, err, unit)
	}

	return nil
}

// cmdRecSignal pauses or resumes a recording, then waits until the
// recorder reports the new state.
func cmdRecSignal(args []string, sig syscall.Signal, paused bool) error {
	n, err := ownRec(args)
	if err != nil {
		return err
	}

	kill := exec.Command("systemctl", "--user", "kill", "--kill-whom=main",
		"--signal="+strconv.Itoa(int(sig)), recUnitName(n))
	kill.Stderr = os.Stderr
	if err := kill.Run(); err != nil {
		return err
	}

	for deadline := time.Now().Add(20 * time.Second); ; {
		r := readRec(n)
		switch {
		case r == nil:
			return fmt.Errorf(":%d: the recording ended (see journalctl "+
				"--user -u %s)", n, recUnitName(n))
		case r.Paused == paused:
			fmt.Printf(":%d %s: %s\n", n, r.state(), r.Video)
			return nil
		case time.Now().After(deadline):
			return fmt.Errorf(":%d: recorder didn't answer in 20s", n)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// cmdRecStop finishes a recording and prints the video.
func cmdRecStop(args []string) error {
	n, err := ownRec(args)
	if err != nil {
		return err
	}

	r := readRec(n)
	if r == nil {
		return fmt.Errorf(":%d isn't recording", n)
	}

	// Blocks until the unit is down: video joined and final
	stop := exec.Command("systemctl", "--user", "stop", recUnitName(n))
	stop.Stderr = os.Stderr
	if err := stop.Run(); err != nil {
		return err
	}

	printRecording(n, r.Video)
	return nil
}

// ownRec parses `[--force] :N` and checks the caller owns that display's
// running recording.
func ownRec(args []string) (int, error) {
	fs := flag.NewFlagSet("rec", flag.ExitOnError)
	force := fs.Bool("force", false, "act on another agent's recording")
	fs.Parse(args)

	if fs.NArg() != 1 {
		return 0, errors.New("usage: desk rec pause|resume|stop " +
			"[--force] :N")
	}

	n, err := parseRecDisplay(fs.Arg(0))
	if err != nil {
		return 0, err
	}

	r := readRec(n)
	if r == nil {
		return 0, fmt.Errorf(":%d isn't recording", n)
	}

	me, err := callerOwner()
	if err != nil {
		return 0, err
	}

	if !*force && !r.Owner.Same(me) {
		return 0, fmt.Errorf(":%d's recording belongs to %s, not you "+
			"(%s); --force to touch it anyway", n, r.Owner, me)
	}

	return n, nil
}

// ---- Recorder ----

// cmdRecRun is a desk-rec-N unit's main process.
func cmdRecRun(args []string) error {
	fs := flag.NewFlagSet("rec run", flag.ExitOnError)
	ownerArg := fs.String("owner", "", "owning process, PID:START")
	video := fs.StringP("out", "o", "", "the finished video")
	fs.Parse(args)

	if fs.NArg() != 1 || *video == "" {
		return errors.New("usage: desk rec run --owner PID:START -o FILE N")
	}

	owner, err := parseOwnerArg(*ownerArg)
	if err != nil {
		return err
	}

	n, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		return err
	}

	r := &recorder{
		n:       n,
		display: ":" + strconv.Itoa(n),
		video:   *video,
		env:     append(os.Environ(), "DISPLAY=:"+strconv.Itoa(n)),
	}

	// Handlers first: a pause sent right after start must not kill us
	// Room for a burst: a pause can block for seconds, and a stop
	// arriving meanwhile must not be dropped
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, recPause, recResume, syscall.SIGTERM, syscall.SIGINT)

	if err := r.resume(); err != nil {
		return err
	}

	state := Recording{Video: *video, Owner: owner}
	if err := writeRec(n, state); err != nil {
		r.finish()
		return err
	}

	if err := notify("READY=1"); err != nil {
		r.finish()
		return err
	}

	ownerGone := make(chan struct{})
	go watchOwner(owner, ownerGone)

	for {
		select {
		case sig := <-sigs:
			switch sig {
			case recPause:
				r.pause()
			case recResume:
				if err := r.resume(); err != nil {
					log.Printf("resume: %v (still paused)", err)
				}
			default:
				return r.finish()
			}

		case <-ownerGone:
			log.Printf("owner %s exited, saving", owner)
			return r.finish()

		case <-r.died():
			// The display went away, or ffmpeg crashed
			log.Printf("ffmpeg exited on its own, saving")
			r.current = nil
			r.finish()
			return errors.New("ffmpeg exited mid-recording")
		}

		state.Paused = r.current == nil
		if err := writeRec(n, state); err != nil {
			log.Printf("state: %v", err)
		}
	}
}

// recorder records one video as a run of segments.
type recorder struct {
	n       int
	display string
	video   string
	env     []string

	parts   []string
	current *segment // nil while paused
}

// resume starts the next segment, if paused.
func (r *recorder) resume() error {
	if r.current != nil {
		return nil
	}

	path := partPath(r.video, len(r.parts)+1)
	seg, err := startSegment(r.display, path, audioInput(r.n), r.env)
	if err != nil {
		return err
	}

	r.current = seg
	r.parts = append(r.parts, path)
	return nil
}

// pause finalizes the current segment, if recording.
func (r *recorder) pause() {
	if r.current != nil {
		r.current.stop()
		r.current = nil
	}
}

// died fires when the current segment's ffmpeg exits; never if paused.
func (r *recorder) died() <-chan struct{} {
	if r.current == nil {
		return nil
	}
	return r.current.done
}

// finish stops grabbing and joins the parts into the video.
func (r *recorder) finish() error {
	r.pause()

	if len(r.parts) == 0 {
		return nil
	}
	return joinSegments(r.video, r.parts)
}

// cmdRecCleanup is desk-rec-N's ExecStopPost: crashes included, the
// state goes. Unjoined parts stay on disk: never delete recordings.
func cmdRecCleanup(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: desk rec cleanup N")
	}

	n, err := strconv.Atoi(args[0])
	if err != nil {
		return err
	}

	os.Remove(recPath(n))
	return nil
}
