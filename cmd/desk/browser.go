package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	flag "github.com/spf13/pflag"
)

// ===== desk browser =====
//
// The agents' browser: browser.command (any Chromium) launched as a
// plain browser with a DevTools port on localhost. Agents attach with
// `agent-browser connect PORT` and drive it with X input on top. Unlike
// a browser agent-browser launches itself, it carries no automation
// flags, so sites see navigator.webdriver false.
//
// A browser is known by its DevTools port, never by scanning processes:
// launchers that fork (flatpak run) work, and the user's own Chromium is
// never touched.

const browserUsage = `usage:
  desk browser start --display :N [--profile P] [URL...]
                         launch browser.command on :N, print its DevTools port
  desk browser status    list running ones; exit 1 if none
  desk browser stop PORT close one gracefully

  --profile temp         fresh and empty, deleted on stop (default)
  --profile main         the user's own (browser.main_profile): their
                         accounts, their settings
  --profile DIR          any other

temp runs in parallel; main and DIR: one browser per profile.
Attach with:  agent-browser connect PORT
Stop it before desk rm: losing its display kills it mid-write.
`

// errStopped makes `status` exit 1: no browser is running.
var errStopped = errors.New("not running")

// cmdBrowser dispatches `desk browser ...`.
func cmdBrowser(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, browserUsage)
		return errors.New("browser: needs a command")
	}

	switch args[0] {
	case "start":
		return cmdBrowserStart(args[1:])
	case "status":
		return cmdBrowserStatus()
	case "stop":
		return cmdBrowserStop(args[1:])
	case "supervise": // internal: start's detached half
		return cmdBrowserSupervise(args[1:])
	case "help", "-h", "--help":
		fmt.Print(browserUsage)
		return nil
	}

	fmt.Fprint(os.Stderr, browserUsage)
	return fmt.Errorf("browser: unknown command %q", args[0])
}

// ---- start ----

func cmdBrowserStart(args []string) error {
	fs := flag.NewFlagSet("browser start", flag.ExitOnError)
	display := fs.String("display", "", "X display, e.g. :20 from desk new")
	profileName := fs.String("profile", "temp", "temp, main or a directory")
	fs.Parse(args)

	// No $DISPLAY fallback: agents inherit the user's :0
	if *display == "" {
		return errors.New("browser start: --display required (desk new " +
			"gives one)")
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

	port, err := freePort()
	if err != nil {
		os.RemoveAll(r.dir)
		return err
	}

	for name, value := range map[string]string{"profile": profile,
		"display": *display, "port": strconv.Itoa(port)} {
		if err := r.set(name, value); err != nil {
			os.RemoveAll(r.dir)
			return err
		}
	}

	if err := r.supervise(lock, fs.Args()); err != nil {
		os.RemoveAll(r.dir)
		return err
	}

	// The supervisor holds the run now: let go, so its exit shows
	lock.Close()

	if err := r.waitStarted(port); err != nil {
		return err
	}

	fmt.Println(port)
	return nil
}

// prepareProfile resolves --profile for run r: a fresh one inside r for
// temp. Refuses a profile a browser has open: Chromium would hand the
// launch to it, and no DevTools port would come up.
func prepareProfile(name string, r browserRun) (string, error) {
	var profile string
	switch name {
	case "temp":
		profile = r.path("temp-profile")
		if err := os.Mkdir(profile, 0o700); err != nil {
			return "", err
		}
		seedWidevine(profile)

	case "main":
		profile = cfg.Browser.MainProfile

	default:
		abs, err := filepath.Abs(name)
		if err != nil {
			return "", err
		}
		profile = abs
	}

	// Chromium's own "profile open" marker, a symlink to host-PID
	if _, err := os.Lstat(filepath.Join(profile, "SingletonLock")); err == nil {
		return "", fmt.Errorf("profile in use: %s has a SingletonLock. "+
			"Close that browser, or --profile temp. Stale after a crash? "+
			"Delete the lock", profile)
	}

	// The user's own settings stay theirs
	if name != "main" {
		if err := quietPasswords(profile); err != nil {
			return "", fmt.Errorf("profile prefs: %w", err)
		}
	}

	return profile, nil
}

// ---- status, stop ----

func cmdBrowserStatus() error {
	all, err := listRuns()
	if err != nil {
		return err
	}

	var runs []browserRun
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

func cmdBrowserStop(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: desk browser stop PORT")
	}

	r, err := findRun(args[0])
	if err != nil {
		return err
	}

	port, _ := strconv.Atoi(r.get("port"))
	if err := closeBrowser(port); err != nil {
		return err
	}

	// The supervisor deletes temp profiles: wait, so they're gone too
	if !waitFor(func() bool { return !r.exists() }, 20*time.Second) {
		return fmt.Errorf("port %d: browser closed, its run lingers: %s",
			port, r.dir)
	}
	return nil
}

// ===== DevTools =====

// closeBrowser asks the browser on port to quit through DevTools
// (Browser.close): a clean exit, session and cookies saved. Already gone
// is fine.
func closeBrowser(port int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	target, err := devToolsSocket(ctx, port)
	if err != nil {
		return nil // nothing listens: nothing to close
	}

	c, _, err := websocket.Dial(ctx, target, nil)
	if err != nil {
		return fmt.Errorf("devtools %d: %w", port, err)
	}
	defer c.CloseNow()

	msg := `{"id":1,"method":"Browser.close"}`
	if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		return fmt.Errorf("devtools %d: Browser.close: %w", port, err)
	}

	gone := func() bool { return !devToolsUp(port) }
	if !waitFor(gone, 15*time.Second) {
		return fmt.Errorf("devtools %d: still up 15 s after Browser.close",
			port)
	}
	return nil
}

// devToolsSocket returns the browser-wide DevTools WebSocket URL.
func devToolsSocket(ctx context.Context, port int) (string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var version struct {
		WebSocket string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&version); err != nil {
		return "", err
	}
	if version.WebSocket == "" {
		return "", errors.New("no webSocketDebuggerUrl")
	}
	return version.WebSocket, nil
}

// devToolsUp reports whether something answers DevTools on port.
func devToolsUp(port int) bool {
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version",
		port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// startWait bounds a launch: how long start waits for DevTools, and the
// supervisor for a forking launcher's browser.
const startWait = 30 * time.Second

// waitStarted waits for run r's DevTools on port. A run whose
// supervisor gave up (the launch failed, leaving its log) fails at
// once, with the log's tail, and goes.
func (r browserRun) waitStarted(port int) error {
	up := func() bool { return devToolsUp(port) }
	waitFor(func() bool { return up() || !r.alive() }, startWait)

	switch {
	case up():
		return nil

	case !r.alive():
		tail := logTail(r.path("log"), 10)
		os.RemoveAll(r.dir)
		return fmt.Errorf("browser didn't start:\n%s", tail)
	}

	closeBrowser(port)
	return fmt.Errorf("no DevTools on port %d after %s (log: %s)", port,
		startWait, r.path("log"))
}

// logTail is the last n lines of the log at path, or why there are none.
func logTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ===== Helpers =====

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
