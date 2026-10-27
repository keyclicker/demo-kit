package main

import (
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
	"syscall"
	"time"
)

// recordDir, under $HOME, is served over Tailscale as /public/recordings.
const recordDir = "public/recordings"

// recordingPath names the video of a desktop started now on display n.
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

// startRecording grabs display n into path with ffmpeg until
// stopRecording. env must carry the display's XAUTHORITY.
func startRecording(n int, path string, env []string) (*exec.Cmd, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	// Encode on the 890M (VAAPI), color conversion included: ~4% of a
	// core at 30 fps, vs ~50% for libx264 veryfast
	ffmpeg := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-vaapi_device", renderNode,
		"-f", "x11grab", "-framerate", "30", "-i", ":"+strconv.Itoa(n),

		// nv12 (yuv420p) plays everywhere but needs even sides
		"-vf", "hwupload,"+
			"scale_vaapi=w=trunc(iw/2)*2:h=trunc(ih/2)*2:format=nv12",
		"-c:v", "h264_vaapi", "-qp", "28",
		"-movflags", "+faststart", path)
	ffmpeg.Env = env
	ffmpeg.Stderr = os.Stderr

	return ffmpeg, ffmpeg.Start()
}

// stopRecording makes ffmpeg finalize the video: an mp4 is unplayable
// until its trailer is written.
func stopRecording(ffmpeg *exec.Cmd) {
	if ffmpeg == nil {
		return
	}

	done := make(chan struct{})
	go func() { ffmpeg.Wait(); close(done) }()

	ffmpeg.Process.Signal(syscall.SIGINT)

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		ffmpeg.Process.Kill()
		<-done
	}
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
