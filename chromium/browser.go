package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// errStopped makes `status` exit 1: the browser isn't running.
var errStopped = errors.New("not running")

const flatpakApp = "org.chromium.Chromium"

// Browser is a running Chromium: its main browser process. Port 0
// means no DevTools: the user's own browser, not one start launched.
type Browser struct {
	PID     int
	Display string
	Port    int
}

// xdgDir returns $env, else ~/fallback.
func xdgDir(env, fallback string) string {
	if dir := os.Getenv(env); dir != "" {
		return dir
	}

	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}

// agentProfile is the agents' own, disposable profile.
func agentProfile() string {
	return filepath.Join(xdgDir("XDG_DATA_HOME", ".local/share"),
		"agent-chromium")
}

// mainProfile is the user's own profile: what their Flatpak Chromium
// opens without --user-data-dir.
func mainProfile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".var/app", flatpakApp, "config/chromium")
}

// stateDir holds start's per-port display and log files, outside the
// profiles: main is the user's.
func stateDir() string {
	return filepath.Join(xdgDir("XDG_STATE_HOME", ".local/state"),
		"agent-chromium")
}

// profileFlag adds -profile (agent, main or a directory) to fs and
// returns its absolute path after fs.Parse.
func profileFlag(fs *flag.FlagSet) func() (string, error) {
	p := fs.String("profile", "agent", "agent, main or a directory")

	return func() (string, error) {
		switch *p {
		case "agent":
			return agentProfile(), nil
		case "main":
			return mainProfile(), nil
		}
		return filepath.Abs(*p)
	}
}

// ===== Finding it =====

// browserProfile returns the profile of argv's Chromium, "" unless
// argv is a main browser process. Flatpak's bwrap wrappers carry the
// same flags, and every child process has a --type=, so match the
// binary too. No --user-data-dir: the user's own, on the main profile.
func browserProfile(argv []string) string {
	if len(argv) == 0 || filepath.Base(argv[0]) != "chrome" {
		return ""
	}

	profile := mainProfile()
	for _, a := range argv[1:] {
		if strings.HasPrefix(a, "--type=") {
			return ""
		}
		if v, ok := strings.CutPrefix(a, "--user-data-dir="); ok {
			profile = v
		}
	}

	return profile
}

// findBrowser scans /proc for profile's browser process, nil if none.
func findBrowser(profile string) (*Browser, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}

		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}

		argv := parseCmdline(raw)
		if browserProfile(argv) == profile {
			return describe(pid, argv), nil
		}
	}

	return nil, nil
}

// parseCmdline splits /proc/PID/cmdline. Chromium rewrites its process
// title into one space-joined string, so split that on spaces: profile
// paths with spaces won't match.
func parseCmdline(raw []byte) []string {
	argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	if len(argv) == 1 {
		return strings.Fields(argv[0])
	}

	return argv
}

// describe fills in a browser's DevTools port, and the display start
// recorded: Chromium's retitling overwrites /proc/PID/environ too.
func describe(pid int, argv []string) *Browser {
	b := &Browser{PID: pid}

	for _, a := range argv {
		if v, ok := strings.CutPrefix(a, "--remote-debugging-port="); ok {
			b.Port, _ = strconv.Atoi(v)
		}
	}

	if b.Port != 0 {
		display, _ := os.ReadFile(stateFile(b.Port, "display"))
		b.Display = strings.TrimSpace(string(display))
	}

	return b
}

// stateFile is start's record for the browser on port: "display", "log".
func stateFile(port int, kind string) string {
	return filepath.Join(stateDir(), fmt.Sprintf("%d.%s", port, kind))
}

func (b *Browser) String() string {
	if b.Port == 0 {
		return fmt.Sprintf("pid %d, the user's own Chromium", b.PID)
	}

	return fmt.Sprintf("pid %d, display %s, port %d", b.PID, b.Display, b.Port)
}

// ===== Commands =====

func cmdStart(args []string) error {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	display := fs.String("display", "", "X display, e.g. :10 from desk new")
	port := fs.Int("port", 9222, "DevTools port on 127.0.0.1")
	profile := profileFlag(fs)
	fs.Parse(args)

	// No $DISPLAY fallback: agents inherit the user's :0
	if *display == "" {
		return errors.New("start: -display required (desk new gives one)")
	}

	dir, err := profile()
	if err != nil {
		return err
	}

	if b, err := findBrowser(dir); err != nil {
		return err
	} else if b != nil {
		return fmt.Errorf("profile in use (%s): one browser per profile", b)
	}

	if portBusy(*port) {
		return fmt.Errorf("port %d is taken: pick another -port", *port)
	}

	// The user's own settings stay theirs
	if dir != mainProfile() {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := quietPasswords(dir); err != nil {
			return fmt.Errorf("profile prefs: %w", err)
		}
	}

	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	err = os.WriteFile(stateFile(*port, "display"), []byte(*display+"\n"), 0o600)
	if err != nil {
		return err
	}

	logPath := stateFile(*port, "log")
	if err := launch(dir, *display, *port, fs.Args(), logPath); err != nil {
		return err
	}

	if err := waitDevTools(*port, 30*time.Second); err != nil {
		return fmt.Errorf("%w (log: %s)", err, logPath)
	}

	fmt.Println(*port)
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	profile := profileFlag(fs)
	fs.Parse(args)

	dir, err := profile()
	if err != nil {
		return err
	}

	b, err := findBrowser(dir)
	if err != nil {
		return err
	}
	if b == nil {
		fmt.Println("stopped")
		return errStopped
	}

	fmt.Println("running:", b)
	return nil
}

func cmdStop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	profile := profileFlag(fs)
	fs.Parse(args)

	dir, err := profile()
	if err != nil {
		return err
	}

	b, err := findBrowser(dir)
	if err != nil || b == nil {
		return err
	}
	if b.Port == 0 {
		return fmt.Errorf("%s: not ours, never stop it", b)
	}

	// TERM closes Chromium cleanly: session, cookies and prefs saved
	if err := syscall.Kill(b.PID, syscall.SIGTERM); err != nil {
		return err
	}
	if waitGone(b.PID, 15*time.Second) {
		return nil
	}

	syscall.Kill(b.PID, syscall.SIGKILL)
	return fmt.Errorf("pid %d ignored TERM for 15 s: killed", b.PID)
}

// ===== Helpers =====

// launch starts Flatpak Chromium detached, in its own session, so it
// outlives the agent's short-lived tool-call shell.
func launch(profile, display string, port int, urls []string, logPath string) error {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	args := []string{
		"run", flatpakApp,
		"--user-data-dir=" + profile,
		"--remote-debugging-port=" + strconv.Itoa(port),
		"--no-first-run",
		"--no-default-browser-check",
		"--start-maximized",
	}
	args = append(args, urls...)

	cmd := exec.Command("flatpak", args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+display)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return err
	}

	return cmd.Process.Release()
}

func portBusy(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}

	conn.Close()
	return true
}

// waitDevTools polls the DevTools endpoint until it answers.
func waitDevTools(port int, timeout time.Duration) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	client := http.Client{Timeout: time.Second}

	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		if resp, err := client.Get(url); err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}

	return fmt.Errorf("no DevTools on port %d after %s", port, timeout)
}

func waitGone(pid int, timeout time.Duration) bool {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}

	return false
}
