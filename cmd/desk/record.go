package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keyclicker/demo-kit/internal/encode"
)

// encoder is how recordings encode, picked once per process: VAAPI
// costs ~4% of a core at 30 fps, x264 veryfast ~50%.
var encoder = sync.OnceValues(func() (encode.Encoder, error) {
	return encode.Pick(cfg.Video)
})

// recordFilter converts screen RGB to what plays everywhere: nv12 or
// yuv420p, even sides, tagged BT.709 limited range. Left alone, the RGB
// source's tags (pc, gbr) stick to YUV frames, and players guess colors.
func recordFilter(enc encode.Encoder) string {
	if enc.VAAPI() {
		return "hwupload," +
			"scale_vaapi=w=trunc(iw/2)*2:h=trunc(ih/2)*2:format=nv12:" +
			"out_range=tv:out_color_matrix=bt709:" +
			"out_color_primaries=bt709:out_color_transfer=bt709"
	}

	return "scale=trunc(iw/2)*2:trunc(ih/2)*2:" +
		"out_range=tv:out_color_matrix=bt709,format=yuv420p," +
		"setparams=range=tv:colorspace=bt709:color_primaries=bt709:" +
		"color_trc=bt709"
}

// recordingPath names a recording of display n started now, in
// record.dir.
func recordingPath(n int) string {
	name := fmt.Sprintf("%s-desk-%d.mp4",
		time.Now().Format("2006-01-02-150405"), n)
	return filepath.Join(cfg.Record.Dir, name)
}

// ---- Recording ----

// segment is one ffmpeg grabbing a display into one mp4. A recording
// is one or more segments: a pause ends one, a resume starts the next.
type segment struct {
	path string
	cmd  *exec.Cmd
	done chan struct{} // closed once ffmpeg exits
}

// startSegment grabs display (":N") into path, with sound from the
// ffmpeg input audio (audioInput; nil: video only). It returns once
// ffmpeg reports its first progress, so a display it can't open fails
// here. env must carry the display's XAUTHORITY.
func startSegment(display, path string, audio, env []string) (*segment,
	error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	enc, err := encoder()
	if err != nil {
		return nil, err
	}

	args := []string{"-nostdin", "-loglevel", "error",
		"-nostats", "-progress", "pipe:1", "-y"}
	args = append(args, enc.Device()...)
	args = append(args, "-f", "x11grab", "-framerate", "30", "-i", display)
	args = append(args, audio...)
	args = append(args, "-vf", recordFilter(enc))
	args = append(args, enc.Codec(28)...)
	if audio != nil {
		args = append(args, "-c:a", "aac", "-b:a", "160k")
	}
	args = append(args, "-movflags", "+faststart", path)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Env = env
	cmd.Stderr = os.Stderr

	progress, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	seg := &segment{path: path, cmd: cmd, done: make(chan struct{})}
	go func() { cmd.Wait(); close(seg.done) }()

	// First progress block (~0.5 s in) = frames are being encoded
	started := make(chan struct{})
	go func() {
		var once sync.Once
		lines := bufio.NewScanner(progress)
		for lines.Scan() {
			if strings.HasPrefix(lines.Text(), "progress=") {
				once.Do(func() { close(started) })
			}
		}
	}()

	select {
	case <-started:
		return seg, nil
	case <-seg.done:
		return nil, fmt.Errorf("ffmpeg on %s exited at start", display)
	case <-time.After(15 * time.Second):
		seg.stop()
		return nil, fmt.Errorf("ffmpeg on %s: no frames after 15s", display)
	}
}

// stop makes ffmpeg finalize the segment: an mp4 is unplayable until
// its trailer is written. One that won't in 20 s is killed, its part
// likely broken (joinSegments checks).
func (s *segment) stop() {
	s.cmd.Process.Signal(syscall.SIGINT)

	select {
	case <-s.done:
	case <-time.After(20 * time.Second):
		log.Printf("ffmpeg didn't finalize %s in 20s: killed", s.path)
		s.cmd.Process.Kill()
		<-s.done
	}
}

// partPath names segment k (from 1) of the recording at path.
func partPath(path string, k int) string {
	ext := filepath.Ext(path)
	return fmt.Sprintf("%s.part%d%s", strings.TrimSuffix(path, ext), k, ext)
}

// joinSegments makes the parts one video at path, losslessly (same
// encoder, same flags, so the concat demuxer can copy), then removes
// the parts. path appears only once it is whole: the join goes to a
// hidden name beside it, renamed on success. On any error the parts
// stay and path doesn't exist.
func joinSegments(path string, parts []string) error {
	// The concat demuxer skips an unreadable part and still exits 0
	for _, p := range parts {
		if err := checkVideo(p); err != nil {
			return err
		}
	}

	if len(parts) == 1 {
		return os.Rename(parts[0], path)
	}

	list, err := os.CreateTemp("", "desk-concat-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(list.Name())

	list.WriteString(concatList(parts))
	list.Close()

	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+
		".joining")
	defer os.Remove(tmp)

	join := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-xerror", "-y", "-f", "concat", "-safe", "0", "-i", list.Name(),
		"-c", "copy", "-movflags", "+faststart", "-f", "mp4", tmp)
	if out, err := join.CombinedOutput(); err != nil {
		return fmt.Errorf("join %d segments: %v: %s", len(parts), err, out)
	}

	if err := os.Rename(tmp, path); err != nil {
		return err
	}

	for _, p := range parts {
		os.Remove(p)
	}
	return nil
}

// checkVideo reads every packet of the video at path, without decoding.
// A probe isn't enough: a part cut short after +faststart moved its
// index up front still probes fine.
func checkVideo(path string) error {
	read := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-xerror", "-i", path, "-map", "0", "-c", "copy", "-f", "null", "-")
	if out, err := read.CombinedOutput(); err != nil {
		return fmt.Errorf("%s is broken: %v: %s", path, err,
			lastLine(string(out)))
	}

	return nil
}

// lastLine is s's last non-empty line: ffmpeg's last word on an error.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// concatList renders an ffmpeg concat demuxer script for files.
func concatList(files []string) string {
	var b strings.Builder
	for _, f := range files {
		quoted := strings.ReplaceAll(f, "'", `'\''`)
		fmt.Fprintf(&b, "file '%s'\n", quoted)
	}
	return b.String()
}

// ---- Frame clock ----

// holdFrameClock keeps one silent VNC client logged in to the desktop.
//
// Xvnc ticks its frame clock (Present's vblank) at 1 Hz while no VNC
// client is authenticated, so vsynced apps (GL, browsers) crawl at
// 1 fps until someone watches. The client never requests updates, so
// Xvnc encodes nothing for it. Close the returned conn to let go.
func holdFrameClock(port int) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		return nil, err
	}

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := rfbHandshake(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vnc handshake: %w", err)
	}
	conn.SetDeadline(time.Time{})

	// Drain whatever Xvnc volunteers (bells, clipboard) so it never blocks
	go io.Copy(io.Discard, conn)

	return conn, nil
}

// rfbHandshake logs in to an RFB 3.8 server with security None and
// reads its ServerInit.
func rfbHandshake(conn net.Conn) error {
	version := make([]byte, 12)
	if _, err := io.ReadFull(conn, version); err != nil {
		return err
	}
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}

	// Security types: count, then the list; pick None (1)
	var count [1]byte
	if _, err := io.ReadFull(conn, count[:]); err != nil {
		return err
	}
	if count[0] == 0 {
		return errors.New("server refused the connection")
	}
	types := make([]byte, count[0])
	if _, err := io.ReadFull(conn, types); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{1}); err != nil {
		return err
	}

	var result uint32
	if err := binary.Read(conn, binary.BigEndian, &result); err != nil {
		return err
	}
	if result != 0 {
		return errors.New("security None rejected")
	}

	// ClientInit: shared, so viewers never kick each other out
	if _, err := conn.Write([]byte{1}); err != nil {
		return err
	}

	// ServerInit: size (4), pixel format (16), then a length-prefixed name
	head := make([]byte, 24)
	if _, err := io.ReadFull(conn, head); err != nil {
		return err
	}
	name := binary.BigEndian.Uint32(head[20:])
	_, err := io.CopyN(io.Discard, conn, int64(name))

	return err
}
