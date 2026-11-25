package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Sound: each desk display gets its own PipeWire sink, desk-N, so its
// recording hears only that display. Apps reach it with
// PULSE_SINK=desk-N (desk browser sets it). The sink is a pw-loopback:
// what plays into it also plays on the default output, so deskview's
// stream and the user's speakers hear it as before.
//
// Recordings read the sink's monitor through ffmpeg's pulse input,
// timestamped by the wall clock as x11grab is, so sound and picture
// stay in step. The host display records the default output's.

// sinkName is display n's PipeWire sink.
func sinkName(n int) string { return fmt.Sprintf("desk-%d", n) }

// startSink runs display n's sink and returns once PipeWire lists it;
// it skips quietly when record.audio is off, with a note when PipeWire
// is missing. The process lives in desk-N's cgroup: systemd reaps it
// with the unit.
func startSink(n int) error {
	if !cfg.Record.Audio {
		return nil
	}
	if _, err := exec.LookPath("pw-loopback"); err != nil {
		log.Print("no pw-loopback (PipeWire): recordings without sound")
		return nil
	}

	name := sinkName(n)
	cmd := exec.Command("pw-loopback", "--name", name,
		"--capture-props", fmt.Sprintf("media.class=Audio/Sink "+
			"node.name=%s node.description=\"desk :%d\"", name, n),

		// Passive: an idle display keeps no output device awake
		"--playback-props", "node.passive=true")
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	if !waitFor(func() bool { return sinkUp(name) }, 5*time.Second) {
		cmd.Process.Kill()
		cmd.Wait()
		return fmt.Errorf("sink %s: not up after 5s", name)
	}

	return nil
}

// sinkUp reports whether PipeWire has a node named name. pw-cli exits 0
// on unknown names too, printing only to stderr: a node has stdout.
func sinkUp(name string) bool {
	out, err := exec.Command("pw-cli", "info", name).Output()
	return err == nil && len(out) > 0
}

// audioInput is the ffmpeg input that records display n's sound, or nil
// (logged once per process) for video only.
func audioInput(n int) []string {
	if err := pulseCheck(); err != nil {
		if !errors.Is(err, errAudioOff) {
			log.Printf("recording without sound: %v", err)
		}
		return nil
	}

	source := "@DEFAULT_MONITOR@"
	if n >= firstDisplay {
		if !sinkUp(sinkName(n)) {
			log.Printf("recording without sound: no sink %s", sinkName(n))
			return nil
		}
		source = sinkName(n) + ".monitor"
	}

	// The queue: ffmpeg reads the pulse input on its own thread, and a
	// slow first video frame must not drop sound
	return []string{"-f", "pulse", "-thread_queue_size", "1024",
		"-name", "desk", "-i", source}
}

// errAudioOff is pulseCheck's answer when record.audio is false.
var errAudioOff = errors.New("off in the config (record.audio)")

// pulseCheck says why recordings can't have sound, or nil: checked once
// per process.
var pulseCheck = sync.OnceValue(func() error {
	if !cfg.Record.Audio {
		return errAudioOff
	}
	if !pulseServer() {
		return errors.New("no Pulse server (pipewire-pulse)")
	}

	help, _ := exec.Command("ffmpeg", "-hide_banner",
		"-h", "demuxer=pulse").Output()
	if !strings.Contains(string(help), "Demuxer pulse") {
		return errors.New("ffmpeg has no pulse input")
	}

	return nil
})

// pulseServer reports whether a Pulse server is reachable the default
// way: $PULSE_SERVER, else its socket in the runtime dir.
func pulseServer() bool {
	if os.Getenv("PULSE_SERVER") != "" {
		return true
	}

	socket := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "pulse", "native")
	return exists(socket)
}
