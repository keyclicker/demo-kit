package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ===== Runs =====
//
// A run is one `desk browser start`: a directory in runsDir() holding
// small files
//
//	profile        the profile's path
//	temp-profile/  --profile temp's profile
//	display        the X display
//	port           the DevTools port
//	log            Chromium's output
//	lock           flock'd by the run's supervisor while it lives
//
// The supervisor removes the whole run, temp profile included, once
// Chromium exits. A run with an unlocked lock lost its supervisor
// (reboot, kill -9): sweepRuns removes it.

// runsDir is in the cache dir, on disk, not $XDG_RUNTIME_DIR: temp
// profiles live in here, too big for RAM, and a sandboxed browser
// (Flatpak) can be granted it: xdg-cache/demo-kit.
func runsDir() string {
	return filepath.Join(cacheDir, "browser")
}

// browserRun is one run's directory.
type browserRun struct{ dir string }

func (r browserRun) path(name string) string {
	return filepath.Join(r.dir, name)
}

// get reads a run file, "" if missing.
func (r browserRun) get(name string) string {
	data, _ := os.ReadFile(r.path(name))
	return strings.TrimSpace(string(data))
}

func (r browserRun) set(name, value string) error {
	return os.WriteFile(r.path(name), []byte(value+"\n"), 0o600)
}

func (r browserRun) exists() bool {
	_, err := os.Stat(r.dir)
	return err == nil
}

// profileLabel is how status shows the profile: temp, main or a path.
func (r browserRun) profileLabel() string {
	switch p := r.get("profile"); p {
	case r.path("temp-profile"):
		return "temp"
	case cfg.Browser.MainProfile:
		return "main"
	default:
		return p
	}
}

// lockStarts takes the lock that serializes starts; call the returned
// func to release it.
func lockStarts() (func(), error) {
	if err := os.MkdirAll(runsDir(), 0o700); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(filepath.Join(runsDir(), "start.lock"),
		os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}

	return func() { f.Close() }, nil
}

// newRun creates a run, its lock already held: the caller hands it to
// the supervisor.
func newRun() (browserRun, *os.File, error) {
	dir, err := os.MkdirTemp(runsDir(), "run-")
	if err != nil {
		return browserRun{}, nil, err
	}
	r := browserRun{dir}

	lock, err := os.OpenFile(r.path("lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		os.RemoveAll(dir)
		return browserRun{}, nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		os.RemoveAll(dir)
		return browserRun{}, nil, err
	}

	return r, lock, nil
}

// listRuns returns every run, alive or not.
func listRuns() ([]browserRun, error) {
	entries, err := os.ReadDir(runsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var runs []browserRun
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "run-") {
			runs = append(runs, browserRun{filepath.Join(runsDir(), e.Name())})
		}
	}

	return runs, nil
}

// findRun returns the run on DevTools port.
func findRun(port string) (browserRun, error) {
	runs, err := listRuns()
	if err != nil {
		return browserRun{}, err
	}

	for _, r := range runs {
		if r.alive() && r.get("port") == port {
			return r, nil
		}
	}

	return browserRun{}, fmt.Errorf("no browser on port %s (see: desk "+
		"browser status)", port)
}

// alive reports whether r's supervisor still holds its lock.
func (r browserRun) alive() bool {
	lock, err := os.Open(r.path("lock"))
	if err != nil {
		return false
	}
	defer lock.Close()

	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	return err != nil
}

// sweepRuns removes runs whose supervisor is gone. Only under
// lockStarts: a start's run has no lock file for a moment.
func sweepRuns() {
	runs, _ := listRuns()

	for _, r := range runs {
		if !r.alive() {
			os.RemoveAll(r.dir)
		}
	}
}

// ===== Supervisor =====

// supervise starts the run's supervisor, detached in its own session
// so it outlives the agent's short-lived tool-call shell. It inherits
// lock, holding the run until the browser exits.
func (r browserRun) supervise(lock *os.File, urls []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}

	logFile, err := os.OpenFile(r.path("log"),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	args := append([]string{"browser", "supervise", r.dir}, urls...)
	cmd := exec.Command(self, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.ExtraFiles = []*os.File{lock} // fd 3
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return err
	}

	return cmd.Process.Release()
}

// cmdBrowserSupervise runs browser.command for run DIR, waits until its
// DevTools port closes, then removes the run: `browser supervise DIR
// [URL...]`, from start only. A launch that never brings the port up
// leaves the run, for start to report its log and remove.
func cmdBrowserSupervise(args []string) error {
	if len(args) < 1 {
		return errors.New("browser supervise: run dir required")
	}
	r := browserRun{args[0]}

	// start's flock. The browser inherits it too, so the run stays alive
	// while either of us does
	lock := os.NewFile(3, "lock")
	defer lock.Close()

	profile := r.get("profile")
	command := cfg.Browser.Command
	browserArgs := append(append([]string{}, command[1:]...),
		"--user-data-dir="+profile,
		"--remote-debugging-port="+r.get("port"),
		"--no-first-run",
		"--no-default-browser-check",
		"--start-maximized",
	)

	display := r.get("display")
	auth, err := xauthority(display)
	if err != nil {
		return err
	}

	cmd := exec.Command(command[0], append(browserArgs, args[1:]...)...)
	cmd.Env = browserEnv(os.Environ(), display, auth)

	// Sound into the display's sink, so its recording hears the page
	if n, err := parseDisplay(display); err == nil && sinkUp(sinkName(n)) {
		cmd.Env = append(cmd.Env, "PULSE_SINK="+sinkName(n))
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr

	port, _ := strconv.Atoi(r.get("port"))
	if err := cmd.Start(); err != nil {
		return err // the run stays, log included, for start to report
	}

	launcher := make(chan struct{})
	seen := watchUp(port, launcher)
	runErr := cmd.Wait()
	close(launcher)

	// A launcher that forks (systemd-run, flatpak run) may return before
	// or while its browser lives: the browser is its port. One that
	// failed never brought it up
	up := <-seen || runErr == nil &&
		waitFor(func() bool { return devToolsUp(port) }, startWait)
	if !up {
		fmt.Fprintf(os.Stderr, "no DevTools on port %d: %v\n", port, runErr)
		return runErr // the run stays, log included, for start to report
	}

	for tcpBound(port) {
		time.Sleep(200 * time.Millisecond)
	}

	if profile == r.path("temp-profile") {
		if err := harvestWidevine(profile); err != nil {
			fmt.Fprintln(os.Stderr, "keeping Widevine:", err)
		}
	}

	os.RemoveAll(r.dir)
	return runErr
}

// watchUp polls DevTools on port until it answers or done closes, then
// sends whether it answered.
func watchUp(port int, done <-chan struct{}) <-chan bool {
	seen := make(chan bool, 1)

	go func() {
		for !devToolsUp(port) {
			select {
			case <-done:
				seen <- devToolsUp(port)
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
		seen <- true
	}()

	return seen
}

// browserEnv is the browser's environment: environ on display, with
// that display's X authority, minus Wayland's variables, with which a
// recent Chromium opens on the user's Wayland desktop instead.
func browserEnv(environ []string, display, auth string) []string {
	env := []string{"DISPLAY=" + display, "XAUTHORITY=" + auth}

	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "DISPLAY", "XAUTHORITY", "WAYLAND_DISPLAY", "XDG_SESSION_TYPE":
			continue
		}
		env = append(env, kv)
	}

	return env
}
