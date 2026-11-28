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
	"time"

	"github.com/keyclicker/demo-kit/internal/cdp"
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
	display := fs.String("display", "", "X display, e.g. :10 from desk new")
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

	if err := waitDevTools(port, 30*time.Second); err != nil {
		closeBrowser(port)
		return fmt.Errorf("%w (log: %s)", err, r.path("log"))
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

	c, err := cdp.Dial(ctx, target)
	if err != nil {
		return fmt.Errorf("port %d: %w", port, err)
	}
	defer c.Close()

	// No answer is fine: the browser may quit first. Its port tells
	c.Call(ctx, "", "Browser.close", nil, nil)

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

// waitDevTools polls the DevTools endpoint until it answers.
func waitDevTools(port int, timeout time.Duration) error {
	if !waitFor(func() bool { return devToolsUp(port) }, timeout) {
		return fmt.Errorf("no DevTools on port %d after %s", port, timeout)
	}
	return nil
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
