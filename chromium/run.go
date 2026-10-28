package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ===== Runs =====
//
// A run is one start: a directory in runsDir() holding small files
//
//	profile        the profile's path
//	temp-profile/  -profile temp's profile
//	display        the X display
//	port           the DevTools port
//	log            Chromium's output
//	lock           flock'd by the run's supervisor while it lives
//
// The supervisor removes the whole run, temp profile included, once
// Chromium exits. A run with an unlocked lock lost its supervisor
// (reboot, kill -9): sweepRuns removes it.

// runsDir is in $HOME, not $XDG_RUNTIME_DIR: Flatpak Chromium sees
// only $HOME, and temp profiles live in here.
func runsDir() string {
	return filepath.Join(xdgDir("XDG_STATE_HOME", ".local/state"),
		"agent-chromium")
}

type run struct{ dir string }

func (r run) path(name string) string {
	return filepath.Join(r.dir, name)
}

// get reads a run file, "" if missing.
func (r run) get(name string) string {
	data, _ := os.ReadFile(r.path(name))
	return strings.TrimSpace(string(data))
}

func (r run) set(name, value string) error {
	return os.WriteFile(r.path(name), []byte(value+"\n"), 0o600)
}

func (r run) exists() bool {
	_, err := os.Stat(r.dir)
	return err == nil
}

// profileLabel is how status shows the profile: temp, main or a path.
func (r run) profileLabel() string {
	switch p := r.get("profile"); p {
	case r.path("temp-profile"):
		return "temp"
	case mainProfile():
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
func newRun() (run, *os.File, error) {
	dir, err := os.MkdirTemp(runsDir(), "run-")
	if err != nil {
		return run{}, nil, err
	}
	r := run{dir}

	lock, err := os.OpenFile(r.path("lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		os.RemoveAll(dir)
		return run{}, nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		os.RemoveAll(dir)
		return run{}, nil, err
	}

	return r, lock, nil
}

// listRuns returns every run, alive or not.
func listRuns() ([]run, error) {
	entries, err := os.ReadDir(runsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var runs []run
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "run-") {
			runs = append(runs, run{filepath.Join(runsDir(), e.Name())})
		}
	}

	return runs, nil
}

// findRun returns the run on DevTools port.
func findRun(port string) (run, error) {
	runs, err := listRuns()
	if err != nil {
		return run{}, err
	}

	for _, r := range runs {
		if r.alive() && r.get("port") == port {
			return r, nil
		}
	}

	return run{}, fmt.Errorf("no browser on port %s (see: status)", port)
}

// alive reports whether r's supervisor still holds its lock.
func (r run) alive() bool {
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
// lock, holding the run until Chromium exits.
func (r run) supervise(lock *os.File, urls []string) error {
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

	cmd := exec.Command(self, append([]string{"supervise", r.dir}, urls...)...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.ExtraFiles = []*os.File{lock} // fd 3
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return err
	}

	return cmd.Process.Release()
}

// cmdSupervise runs Chromium for run DIR in the foreground, then
// removes the run: `supervise DIR [URL...]`, from start only.
func cmdSupervise(args []string) error {
	if len(args) < 1 {
		return errors.New("supervise: run dir required")
	}
	r := run{args[0]}

	// start's flock. Chromium inherits it too, so the run stays alive
	// while either of us does
	lock := os.NewFile(3, "lock")
	defer lock.Close()

	profile := r.get("profile")
	chromiumArgs := []string{
		"run", flatpakApp,
		"--user-data-dir=" + profile,
		"--remote-debugging-port=" + r.get("port"),
		"--no-first-run",
		"--no-default-browser-check",
		"--start-maximized",
	}

	cmd := exec.Command("flatpak", append(chromiumArgs, args[1:]...)...)
	cmd.Env = append(os.Environ(), "DISPLAY="+r.get("display"))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	runErr := cmd.Run()

	// flatpak run can return before Chromium does: wait for it
	waitFor(func() bool {
		b, _ := findBrowser(profile)
		return b == nil
	}, 30*time.Second)

	if profile == r.path("temp-profile") {
		if err := harvestWidevine(profile); err != nil {
			fmt.Fprintln(os.Stderr, "keeping Widevine:", err)
		}
	}

	os.RemoveAll(r.dir)
	return runErr
}
