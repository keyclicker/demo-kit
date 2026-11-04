package main

import (
	"errors"
	"fmt"
	flag "github.com/spf13/pflag"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// errStopped makes `status` exit 1: no browser is running.
var errStopped = errors.New("not running")

const flatpakApp = "org.chromium.Chromium"

// xdgDir returns $env, else ~/fallback.
func xdgDir(env, fallback string) string {
	if dir := os.Getenv(env); dir != "" {
		return dir
	}

	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}

// mainProfile is the user's own profile: what their Flatpak Chromium
// opens without --user-data-dir.
func mainProfile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".var/app", flatpakApp, "config/chromium")
}

// ===== Finding it =====

// Browser is a running Chromium: its main browser process. Ours means
// it has a DevTools port, so start launched it: never stop the rest.
type Browser struct {
	PID  int
	Ours bool
}

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
		if browserProfile(argv) != profile {
			continue
		}

		b := &Browser{PID: pid}
		for _, a := range argv {
			if strings.HasPrefix(a, "--remote-debugging-port=") {
				b.Ours = true
			}
		}
		return b, nil
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

// ===== Commands =====

func cmdStart(args []string) error {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	display := fs.String("display", "", "X display, e.g. :10 from desk new")
	profileName := fs.String("profile", "temp", "temp, main or a directory")
	fs.Parse(args)

	// No $DISPLAY fallback: agents inherit the user's :0
	if *display == "" {
		return errors.New("start: --display required (desk new gives one)")
	}

	// One start at a time: no two pick the same port or profile
	unlock, err := lockStarts()
	if err != nil {
		return err
	}
	defer unlock()

	sweepRuns()

	r, lock, err := newRun()
	if err != nil {
		return err
	}
	defer lock.Close()

	profile, err := prepareProfile(*profileName, r)
	if err != nil {
		os.RemoveAll(r.dir)
		return err
	}

	if err := r.set("profile", profile); err != nil {
		return err
	}
	if err := r.set("display", *display); err != nil {
		return err
	}

	port, err := freePort()
	if err != nil {
		return err
	}
	if err := r.set("port", strconv.Itoa(port)); err != nil {
		return err
	}

	if err := r.supervise(lock, fs.Args()); err != nil {
		os.RemoveAll(r.dir)
		return err
	}

	if err := waitDevTools(port, 30*time.Second); err != nil {
		stopBrowser(profile)
		return fmt.Errorf("%w (log: %s)", err, r.path("log"))
	}

	fmt.Println(port)
	return nil
}

// prepareProfile resolves --profile for run r: a fresh one inside r for
// temp. Refuses a profile some browser has open: Chromium would hand
// the launch to it.
func prepareProfile(name string, r run) (string, error) {
	var profile string
	switch name {
	case "temp":
		profile = r.path("temp-profile")
		if err := os.Mkdir(profile, 0o700); err != nil {
			return "", err
		}
		seedWidevine(profile)

	case "main":
		profile = mainProfile()

	default:
		abs, err := filepath.Abs(name)
		if err != nil {
			return "", err
		}
		profile = abs
	}

	if b, err := findBrowser(profile); err != nil {
		return "", err
	} else if b != nil {
		return "", fmt.Errorf("profile in use (pid %d): one browser per "+
			"profile, or --profile temp", b.PID)
	}

	// The user's own settings stay theirs
	if profile != mainProfile() {
		if err := quietPasswords(profile); err != nil {
			return "", fmt.Errorf("profile prefs: %w", err)
		}
	}

	return profile, nil
}

func cmdStatus() error {
	all, err := listRuns()
	if err != nil {
		return err
	}

	var runs []run
	for _, r := range all {
		if r.alive() {
			runs = append(runs, r)
		}
	}
	if len(runs) == 0 {
		fmt.Println("none running")
		return errStopped
	}

	fmt.Println("PORT\tDISPLAY\tPROFILE")
	for _, r := range runs {
		fmt.Printf("%s\t%s\t%s\n",
			r.get("port"), r.get("display"), r.profileLabel())
	}

	return nil
}

func cmdStop(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: agent-chromium stop PORT")
	}

	r, err := findRun(args[0])
	if err != nil {
		return err
	}

	if err := stopBrowser(r.get("profile")); err != nil {
		return err
	}

	// The supervisor deletes temp profiles: wait, so they're gone too
	waitFor(func() bool { return !r.exists() }, 15*time.Second)
	return nil
}

// ===== Helpers =====

// stopBrowser closes profile's browser, if start launched it. TERM
// closes Chromium cleanly (session, cookies, prefs saved); KILL after
// 15 s.
func stopBrowser(profile string) error {
	b, err := findBrowser(profile)
	if err != nil || b == nil {
		return err
	}
	if !b.Ours {
		return fmt.Errorf("pid %d is the user's own Chromium: never stop it",
			b.PID)
	}

	if err := syscall.Kill(b.PID, syscall.SIGTERM); err != nil {
		return err
	}
	gone := func() bool { return syscall.Kill(b.PID, 0) != nil }
	if waitFor(gone, 15*time.Second) {
		return nil
	}

	syscall.Kill(b.PID, syscall.SIGKILL)
	return fmt.Errorf("pid %d ignored TERM for 15 s: killed", b.PID)
}

// freePort asks the kernel for a free localhost port. Not
// --remote-debugging-port=0: Chromium takes that for chromedriver and
// sets navigator.webdriver.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitDevTools polls the DevTools endpoint until it answers.
func waitDevTools(port int, timeout time.Duration) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	client := http.Client{Timeout: time.Second}

	answers := func() bool {
		resp, err := client.Get(url)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	}

	if !waitFor(answers, timeout) {
		return fmt.Errorf("no DevTools on port %d after %s", port, timeout)
	}

	return nil
}

// waitFor polls cond until it holds or timeout passes.
func waitFor(cond func() bool, timeout time.Duration) bool {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		if cond() {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}

	return cond()
}
