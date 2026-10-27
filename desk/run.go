package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// renderNode is the 890M's render-only DRM node: Xvnc takes GPU
	// buffers from apps through DRI3 here, so GL never hits llvmpipe.
	renderNode = "/dev/dri/renderD128"

	background = "#2b2f36"
)

// cmdRun is the main process of a desk-N unit: it runs Xvnc and xfwm4,
// registers the noVNC token, and exits once its owner does. `desk
// cleanup` undoes it on stop.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	ownerArg := fs.String("owner", "", "owning process, PID:START")
	fs.Parse(args)
	args = fs.Args()

	if len(args) != 2 {
		return errors.New("usage: desk run -owner PID:START N WxH")
	}

	owner, err := parseOwnerArg(*ownerArg)
	if err != nil {
		return err
	}

	n, err := parseDisplay(args[0])
	if err != nil {
		return err
	}

	size := args[1]
	if !sizeRE.MatchString(size) {
		return fmt.Errorf("size %q: want WxH", size)
	}

	display := ":" + strconv.Itoa(n)

	// ---- X authority ----

	// Agents' clients read ~/.Xauthority by default, so the cookie goes
	// there; Xvnc gets its own copy in the private runtime dir.
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	userAuth := filepath.Join(home, ".Xauthority")
	serverAuth := serverAuthPath(n)

	cookie := make([]byte, 16)
	rand.Read(cookie)

	for _, file := range []string{serverAuth, userAuth} {
		err := xauth(file, "add", display, "MIT-MAGIC-COOKIE-1",
			hex.EncodeToString(cookie))
		if err != nil {
			return err
		}
	}

	// ---- X server ----

	xvnc := exec.Command("Xtigervnc", display,
		"-auth", serverAuth,
		"-geometry", size, "-depth", "24",
		"-rendernode", renderNode,
		"-localhost", "-rfbport", strconv.Itoa(basePort+n),
		"-SecurityTypes", "None", "-AlwaysShared",
		"-desktop", "desk "+display,

		// View-only for VNC: agents drive it over X (XTEST), not VNC
		"-AcceptKeyEvents=0", "-AcceptPointerEvents=0",
		"-AcceptCutText=0", "-AcceptSetDesktopSize=0",
	)
	xvnc.Stdout = os.Stderr
	xvnc.Stderr = os.Stderr

	if err := xvnc.Start(); err != nil {
		return err
	}

	exited := make(chan error, 1)
	go func() { exited <- xvnc.Wait() }()

	if err := waitForServer(n, exited); err != nil {
		return err
	}

	// ---- Session ----

	env := append(os.Environ(), "DISPLAY="+display, "XAUTHORITY="+userAuth)

	root := exec.Command("xsetroot", "-solid", background)
	root.Env = env
	if err := root.Run(); err != nil {
		log.Printf("xsetroot: %v", err)
	}

	// Compositing off: Xvnc's DRI3 path glitches under compositors.
	// Systemd reaps it with the unit's cgroup.
	wm := exec.Command("xfwm4", "--compositor=off", "--sm-client-disable")
	wm.Env = env
	if err := wm.Start(); err != nil {
		log.Printf("xfwm4: %v", err)
	}

	// ---- Register ----

	if err := writeToken(n, owner); err != nil {
		return err
	}

	if err := notify("READY=1"); err != nil {
		return err
	}

	// ---- Wait ----

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	ownerGone := make(chan struct{})
	go watchOwner(owner, ownerGone)

	select {
	case <-stop:
		xvnc.Process.Signal(syscall.SIGTERM)
		<-exited
		return nil

	case <-ownerGone:
		log.Printf("owner %s exited, closing", owner)
		xvnc.Process.Signal(syscall.SIGTERM)
		<-exited
		return nil

	case err := <-exited:
		return fmt.Errorf("Xtigervnc exited: %v", err)
	}
}

// waitForServer blocks until display n accepts connections, or fails
// when Xvnc exits or takes too long.
func waitForServer(n int, exited <-chan error) error {
	socket := fmt.Sprintf("/tmp/.X11-unix/X%d", n)
	deadline := time.After(15 * time.Second)

	for !exists(socket) {
		select {
		case err := <-exited:
			return fmt.Errorf("Xtigervnc exited during startup: %v", err)
		case <-deadline:
			return errors.New("Xtigervnc: no X socket after 15s")
		case <-time.After(50 * time.Millisecond):
		}
	}

	return nil
}

// watchOwner closes gone once the owner process exits.
func watchOwner(owner Owner, gone chan<- struct{}) {
	for owner.Alive() {
		time.Sleep(2 * time.Second)
	}

	close(gone)
}

// writeToken registers display n with websockify, atomically.
func writeToken(n int, owner Owner) error {
	tmp := tokenPath(n) + ".tmp"
	data := []byte(tokenFile(n, owner))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}

	return os.Rename(tmp, tokenPath(n))
}

// xauth runs `xauth -q -f file args...`; xauth locks the file itself.
func xauth(file string, args ...string) error {
	cmd := exec.Command("xauth", append([]string{"-q", "-f", file}, args...)...)
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("xauth %v: %w", args, err)
	}

	return nil
}

// notify sends an sd_notify state to systemd, if it is listening.
func notify(state string) error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}

	// Go maps a leading "@" to Linux's abstract socket namespace
	conn, err := net.Dial("unixgram", socket)
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = conn.Write([]byte(state))
	return err
}

// cmdCleanup is a desk-N unit's ExecStopPost: it runs after every stop,
// crashes included, and removes what cmdRun registered.
func cmdCleanup(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: desk cleanup N")
	}

	n, err := parseDisplay(args[0])
	if err != nil {
		return err
	}

	os.Remove(tokenPath(n))
	os.Remove(serverAuthPath(n))

	if home, err := os.UserHomeDir(); err == nil {
		xauth(filepath.Join(home, ".Xauthority"), "remove",
			":"+strconv.Itoa(n))
	}

	// A killed Xvnc leaves its lock and socket; keep a live server's
	lock := fmt.Sprintf("/tmp/.X%d-lock", n)
	if !lockHolderAlive(lock) {
		os.Remove(lock)
		os.Remove(fmt.Sprintf("/tmp/.X11-unix/X%d", n))
	}

	return nil
}

// serverAuthPath is Xvnc's private copy of display n's cookie.
func serverAuthPath(n int) string {
	return filepath.Join(os.Getenv("XDG_RUNTIME_DIR"),
		fmt.Sprintf("desk-%d.xauth", n))
}

// lockHolderAlive reports whether the PID in an X lock file still runs.
func lockHolderAlive(lock string) bool {
	data, err := os.ReadFile(lock)
	if err != nil {
		return false
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false
	}

	return syscall.Kill(pid, 0) == nil
}
