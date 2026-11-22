package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// recordDir, under $HOME, is served over Tailscale as /public/recordings.
const recordDir = "public/recordings"

// recordingPath names a recording of display n started now.
func recordingPath(n int) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	name := fmt.Sprintf("%s-desk-%d.mp4", time.Now().Format("2006-01-02-150405"), n)
	return filepath.Join(home, recordDir, name), nil
}

// recordingURL is where the tailnet reaches a video in ~/public: hub
// serves that dir at /files/. Empty if tailscale can't name this node.
func recordingURL(video string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	rel, err := filepath.Rel(filepath.Join(home, "public"), video)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}

	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return ""
	}

	var status struct{ Self struct{ DNSName string } }
	if json.Unmarshal(out, &status) != nil || status.Self.DNSName == "" {
		return ""
	}

	u := url.URL{
		Scheme: "https",
		Host:   strings.TrimSuffix(status.Self.DNSName, "."),
		Path:   "/files/" + filepath.ToSlash(rel),
	}
	return u.String()
}

// ---- Recording ----

// segment is one ffmpeg grabbing a display into one mp4. A recording
// is one or more segments: a pause ends one, a resume starts the next.
type segment struct {
	path string
	cmd  *exec.Cmd
	done chan struct{} // closed once ffmpeg exits
}

// startSegment grabs display (":N") into path. It returns once ffmpeg
// reports its first progress, so a display it can't open fails here.
// env must carry the display's XAUTHORITY.
func startSegment(display, path string, env []string) (*segment, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	// Encode on the 890M (VAAPI), color conversion included: ~4% of a
	// core at 30 fps, vs ~50% for libx264 veryfast
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-nostats", "-progress", "pipe:1", "-y",
		"-vaapi_device", renderNode,
		"-f", "x11grab", "-framerate", "30", "-i", display,

		// nv12 (yuv420p) plays everywhere but needs even sides. Tagged
		// BT.709 limited range: left alone, the RGB source's tags (pc,
		// gbr) stick to YUV frames, and players guess colors
		"-vf", "hwupload,"+
			"scale_vaapi=w=trunc(iw/2)*2:h=trunc(ih/2)*2:format=nv12:"+
			"out_range=tv:out_color_matrix=bt709:"+
			"out_color_primaries=bt709:out_color_transfer=bt709",
		"-c:v", "h264_vaapi", "-qp", "28",
		"-movflags", "+faststart", path)
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
// its trailer is written.
func (s *segment) stop() {
	s.cmd.Process.Signal(syscall.SIGINT)

	select {
	case <-s.done:
	case <-time.After(20 * time.Second):
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
// the parts.
func joinSegments(path string, parts []string) error {
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

	join := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y",
		"-f", "concat", "-safe", "0", "-i", list.Name(),
		"-c", "copy", "-movflags", "+faststart", path)
	if out, err := join.CombinedOutput(); err != nil {
		return fmt.Errorf("join %d segments: %v: %s", len(parts), err, out)
	}

	for _, p := range parts {
		os.Remove(p)
	}
	return nil
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
