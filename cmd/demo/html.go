package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/keyclicker/demo-kit/internal/cdp"
	flag "github.com/spf13/pflag"
)

// HTML scenes: a page, rendered by headless Chromium (browser.command)
// to a picture, then shown as an image is: paged if tall, a step's box
// spotlit and its zoom zoomed into. Steps may point with CSS selectors,
// and each step renders the page anew with <html data-step="k">, so a
// diagram builds up: elements with data-from / data-to show only on
// those steps.
//
// Fast: one browser per build, started alongside speech; a tab per
// scene, in parallel; pages from a loopback server, so any browser
// (Flatpak's too) loads them and their assets. A built-in stylesheet
// (html/demo.css: the video's palette and font) sits under the page's
// own, in a cascade layer: the page's styles always win.

// ---- Built-ins ----

var (
	//go:embed html/demo.css
	demoCSS string

	// newDocument runs before a page's own scripts: the built-in
	// stylesheet, adopted (no tag to write, nothing to load)
	newDocument = fmt.Sprintf(`if (window.top === window) {
	const sheet = new CSSStyleSheet();
	sheet.replaceSync(%s);
	document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
}`, jsString(demoCSS))
)

// pageReady waits for what a screenshot needs: fonts, the page's own
// window.demoReady promise if it sets one (charts, mermaid), and two
// frames painted.
const pageReady = `(async () => {
	await document.fonts.ready;
	if (window.demoReady) await window.demoReady;
	await new Promise(r => requestAnimationFrame(() =>
		requestAnimationFrame(r)));
})()`

// pageState sets step k (0: none, everything shown) and measures the
// page: its size, the last step its data-from / data-to name, its ids,
// and each selector's box: what its matches cover, in page pixels.
const pageState = `((step, sels) => {
	const root = document.documentElement;
	if (step > 0) root.dataset.step = step;
	else delete root.dataset.step;

	let last = 0;
	for (const el of document.querySelectorAll('[data-from],[data-to]')) {
		const from = +(el.dataset.from || 1), to = +(el.dataset.to || 1e9);
		last = Math.max(last, from, to < 1e9 ? to : 0);
		el.toggleAttribute('data-demo-off',
			step > 0 && (step < from || step > to));
	}

	const box = els => {
		let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
		for (const el of els) {
			const r = el.getBoundingClientRect();
			if (!r.width || !r.height) continue;
			x0 = Math.min(x0, r.left + scrollX);
			y0 = Math.min(y0, r.top + scrollY);
			x1 = Math.max(x1, r.right + scrollX);
			y1 = Math.max(y1, r.bottom + scrollY);
		}
		if (x0 === Infinity) return null;
		x0 = Math.floor(x0), y0 = Math.floor(y0);
		return [x0, y0, Math.ceil(x1) - x0, Math.ceil(y1) - y0];
	};

	const boxes = sels.map(sel => {
		let els;
		try { els = document.querySelectorAll(sel); }
		catch { return {error: 'not a CSS selector'}; }
		if (!els.length) return {error: 'matches nothing'};
		const rect = box(els);
		return rect ? {rect} : {error: 'matches only empty elements'};
	});

	const ids = [...document.querySelectorAll('[id]')]
		.map(el => ({id: el.id, rect: box([el])}))
		.filter(i => i.rect);

	return {
		width: Math.max(root.scrollWidth, document.body?.scrollWidth || 0),
		height: Math.max(root.scrollHeight, document.body?.scrollHeight || 0),
		last, boxes, ids,
	};
})`

// pageTimeout caps loading a page, its fonts and demoReady included.
const pageTimeout = 30 * time.Second

// ---- Renderer ----

// renderer is a headless Chromium and the server it loads pages from.
type renderer struct {
	cmd     *exec.Cmd
	conn    *cdp.Conn
	server  *pageServer
	profile string
}

// startRenderer launches browser.command headless on a profile of its
// own (in demo.scratch: a sandboxed browser can reach it) and connects
// to its DevTools.
func startRenderer(ctx context.Context) (*renderer, error) {
	scratch, err := scratchDir()
	if err != nil {
		return nil, err
	}
	profile, err := os.MkdirTemp(scratch, "chromium-")
	if err != nil {
		return nil, err
	}

	server, err := startPageServer()
	if err != nil {
		os.RemoveAll(profile)
		return nil, err
	}
	r := &renderer{server: server, profile: profile}

	// Port 0: the browser picks one and says it on stderr, so there's
	// no race for a port, and no file inside a sandbox to read
	args := append(slices.Clone(cfg.Browser.Command[1:]), "--headless",
		"--remote-debugging-port=0", "--user-data-dir="+profile,
		"--no-first-run", "--no-default-browser-check", "--hide-scrollbars",
		"--mute-audio", "--force-color-profile=srgb",
		fmt.Sprintf("--window-size=%d,%d", frameW, frameH), "about:blank")
	r.cmd = exec.CommandContext(ctx, cfg.Browser.Command[0], args...)
	r.cmd.WaitDelay = 5 * time.Second

	stderr, err := r.cmd.StderrPipe()
	if err != nil {
		r.close()
		return nil, err
	}
	if err := r.cmd.Start(); err != nil {
		r.close()
		return nil, fmt.Errorf("browser.command: %w", err)
	}

	url, err := devToolsURL(stderr, 30*time.Second)
	if err == nil {
		r.conn, err = cdp.Dial(ctx, url)
	}
	if err != nil {
		r.close()
		return nil, fmt.Errorf("headless %s: %w", cfg.Browser.Command[0], err)
	}
	return r, nil
}

// devToolsRE is Chromium's line naming its DevTools socket.
var devToolsRE = regexp.MustCompile(`DevTools listening on (ws://\S+)`)

// devToolsURL reads stderr up to the DevTools line, then drains the
// rest in the background (a full pipe would stall the browser). On a
// timeout or exit, the error quotes stderr's last lines.
func devToolsURL(stderr io.Reader, timeout time.Duration) (string, error) {
	found := make(chan string, 1)
	var tail []string
	var mu sync.Mutex

	go func() {
		sc := bufio.NewScanner(stderr)
		sent := false
		for sc.Scan() {
			if m := devToolsRE.FindStringSubmatch(sc.Text()); m != nil &&
				!sent {
				found <- m[1]
				sent = true
				continue
			}

			mu.Lock()
			tail = append(tail, sc.Text())
			if len(tail) > 5 {
				tail = tail[1:]
			}
			mu.Unlock()
		}
		if !sent {
			close(found)
		}
	}()

	select {
	case url, ok := <-found:
		if ok {
			return url, nil
		}
	case <-time.After(timeout):
	}

	mu.Lock()
	defer mu.Unlock()
	return "", fmt.Errorf("no DevTools: %s", strings.Join(tail, "; "))
}

// close quits the browser (killed if it lingers), stops the server and
// removes the profile.
func (r *renderer) close() {
	if r.conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		r.conn.Call(ctx, "", "Browser.close", nil, nil) // it may quit first
		cancel()
		r.conn.Close()
	}

	if r.cmd != nil && r.cmd.Process != nil {
		exited := make(chan struct{})
		go func() { r.cmd.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			r.cmd.Process.Kill()
			<-exited
		}
	}

	r.server.close()
	os.RemoveAll(r.profile)
}

// ---- Tabs ----

// tab is one page loaded in the renderer, on its own session.
type tab struct {
	r       *renderer
	target  string
	session string
	path    string

	errors <-chan json.RawMessage // the page's uncaught exceptions
	stop   func()
}

// open loads the page at path in a new frame-sized tab, the built-in
// stylesheet under its own, and waits until it's ready (pageReady).
func (r *renderer) open(ctx context.Context, path string) (*tab, error) {
	url, err := r.server.url(path)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()

	var created struct {
		TargetID string `json:"targetId"`
	}
	err = r.conn.Call(ctx, "", "Target.createTarget",
		map[string]any{"url": "about:blank"}, &created)
	if err != nil {
		return nil, err
	}

	var attached struct {
		SessionID string `json:"sessionId"`
	}
	err = r.conn.Call(ctx, "", "Target.attachToTarget",
		map[string]any{"targetId": created.TargetID, "flatten": true},
		&attached)
	if err != nil {
		return nil, err
	}

	t := &tab{r: r, target: created.TargetID, session: attached.SessionID,
		path: path}
	t.errors, t.stop = r.conn.Events(t.session, "Runtime.exceptionThrown", 8)
	loaded, stopLoad := r.conn.Events(t.session, "Page.loadEventFired", 1)
	defer stopLoad()

	if err := t.load(ctx, url, loaded); err != nil {
		t.close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

// load sets the tab up (frame-sized viewport, the built-ins), goes to
// url and waits for it.
func (t *tab) load(ctx context.Context, url string,
	loaded <-chan json.RawMessage) error {

	calls := []struct {
		method string
		params any
	}{
		{"Page.enable", nil},
		{"Runtime.enable", nil},
		{"Page.addScriptToEvaluateOnNewDocument",
			map[string]any{"source": newDocument}},
		{"Emulation.setDeviceMetricsOverride", map[string]any{
			"width": frameW, "height": frameH, "deviceScaleFactor": 1,
			"mobile": false}},
	}
	for _, c := range calls {
		if err := t.call(ctx, c.method, c.params, nil); err != nil {
			return err
		}
	}

	var nav struct {
		ErrorText string `json:"errorText"`
	}
	if err := t.call(ctx, "Page.navigate", map[string]any{"url": url},
		&nav); err != nil {
		return err
	}
	if nav.ErrorText != "" {
		return errors.New(nav.ErrorText)
	}

	select {
	case <-loaded:
	case <-ctx.Done():
		return fmt.Errorf("not loaded in %s", pageTimeout)
	}

	_, err := t.eval(ctx, pageReady)
	return err
}

// snapshot is the page in one state: its screenshot and what pageState
// measured.
type snapshot struct {
	png    []byte
	width  int
	height int
	last   int // the last step data-from / data-to name; 0: none
	ids    []pageID
}

// pageID is an element with an id, and its box in page pixels.
type pageID struct {
	ID   string `json:"id"`
	Rect []int  `json:"rect"`
}

// snap shows step k (0: none) and screenshots the whole page, the
// frame's width and as tall as it is. Selector regions are resolved to
// their rects, in place.
func (t *tab) snap(ctx context.Context, step int, regions ...*Region) (
	snapshot, error) {

	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()

	sels := []string{} // JSON [], not null
	for _, r := range regions {
		if r != nil && r.Selector != "" {
			sels = append(sels, r.Selector)
		}
	}

	expr := fmt.Sprintf("%s(%d, %s)", pageState, step, jsString(sels))
	raw, err := t.eval(ctx, expr)
	if err != nil {
		return snapshot{}, err
	}

	var state struct {
		Width  int      `json:"width"`
		Height int      `json:"height"`
		Last   int      `json:"last"`
		IDs    []pageID `json:"ids"`
		Boxes  []struct {
			Rect  []int  `json:"rect"`
			Error string `json:"error"`
		} `json:"boxes"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return snapshot{}, err
	}

	s := snapshot{width: state.Width, height: max(state.Height, frameH),
		last: state.Last, ids: state.IDs}
	if s.height > maxDrawH {
		return s, fmt.Errorf("%d px tall, over %d: split it", s.height,
			maxDrawH)
	}

	// Each selector's box, kept on the page
	k := 0
	for _, r := range regions {
		if r == nil || r.Selector == "" {
			continue
		}
		b := state.Boxes[k]
		k++
		if b.Error != "" {
			return s, fmt.Errorf("%s: %s", r, b.Error)
		}
		if r.Rect = clampRect(b.Rect, frameW, s.height); r.Rect == nil {
			return s, fmt.Errorf("%s: off the page", r)
		}
	}

	var shot struct {
		Data []byte `json:"data"` // base64 in JSON: decoded by encoding/json
	}
	err = t.call(ctx, "Page.captureScreenshot", map[string]any{
		"format": "png", "captureBeyondViewport": true,
		"clip": map[string]any{"x": 0, "y": 0, "width": frameW,
			"height": s.height, "scale": 1},
	}, &shot)
	s.png = shot.Data
	return s, err
}

// warnErrors prints the page's uncaught exceptions so far, as where's.
func (t *tab) warnErrors(w io.Writer, where string) {
	for {
		select {
		case raw := <-t.errors:
			var e struct {
				Details struct {
					Text      string `json:"text"`
					Exception struct {
						Description string `json:"description"`
					} `json:"exception"`
				} `json:"exceptionDetails"`
			}
			json.Unmarshal(raw, &e)
			msg := cmp.Or(e.Details.Exception.Description, e.Details.Text)
			msg, _, _ = strings.Cut(msg, "\n") // the message, no stack
			fmt.Fprintf(w, "demo: %s: page error: %s\n", where, msg)
		default:
			return
		}
	}
}

// close closes the tab.
func (t *tab) close() {
	t.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	t.r.conn.Call(ctx, "", "Target.closeTarget",
		map[string]any{"targetId": t.target}, nil)
}

// call runs a method on the tab's session.
func (t *tab) call(ctx context.Context, method string, params,
	result any) error {
	return t.r.conn.Call(ctx, t.session, method, params, result)
}

// eval runs expr in the page, awaiting a promise, and returns its
// value as JSON; a thrown error or a rejection is an error.
func (t *tab) eval(ctx context.Context, expr string) (json.RawMessage,
	error) {

	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	err := t.call(ctx, "Runtime.evaluate", map[string]any{
		"expression": expr, "awaitPromise": true, "returnByValue": true,
	}, &out)
	if err != nil {
		return nil, err
	}
	if e := out.Exception; e != nil {
		msg, _, _ := strings.Cut(cmp.Or(e.Exception.Description, e.Text),
			"\n")
		return nil, errors.New(msg)
	}
	return out.Result.Value, nil
}

// ---- Server ----

// pageServer serves pages to the renderer on a loopback port: each
// page's directory under /p/N/ (its assets beside it or below), the
// built-in font under /.demo/. Missing files warn: a page drawn without
// its picture would pass for done.
type pageServer struct {
	srv  *http.Server
	base string

	mu   sync.Mutex
	dirs []string
}

// startPageServer listens on a free loopback port.
func startPageServer() (*pageServer, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	s := &pageServer{base: "http://" + l.Addr().String()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.demo/mono.ttf", serveFont(monoRegular))
	mux.HandleFunc("GET /.demo/mono-bold.ttf", serveFont(monoBold))
	mux.HandleFunc("GET /p/{n}/{path...}", s.file)

	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go s.srv.Serve(l)
	return s, nil
}

// serveFont serves a TTF.
func serveFont(ttf []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "font/ttf")
		w.Write(ttf)
	}
}

// url is where the renderer loads the page at path; its directory is
// mounted on first use.
func (s *pageServer) url(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	n := slices.Index(s.dirs, filepath.Dir(abs))
	if n < 0 {
		n = len(s.dirs)
		s.dirs = append(s.dirs, filepath.Dir(abs))
	}
	return fmt.Sprintf("%s/p/%d/%s", s.base, n, filepath.Base(abs)), nil
}

// file serves a file from mounted directory n, warning when it's
// missing (favicons aside).
func (s *pageServer) file(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var dir string
	if n, err := strconv.Atoi(r.PathValue("n")); err == nil &&
		n < len(s.dirs) {
		dir = s.dirs[n]
	}
	s.mu.Unlock()

	rel := r.PathValue("path")
	if dir == "" {
		http.NotFound(w, r)
		return
	}

	// http.Dir keeps requests inside dir
	f, err := http.Dir(dir).Open("/" + rel)
	if err != nil {
		if rel != "favicon.ico" {
			fmt.Fprintf(os.Stderr, "demo: a page asked for %s: %v\n",
				filepath.Join(dir, rel), errors.Unwrap(err))
		}
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, rel, st.ModTime(), f)
}

// close stops serving.
func (s *pageServer) close() { s.srv.Close() }

// ---- Scenes ----

// page is an html scene rendered: a screenshot per step (one without
// steps), and its steps with their selectors resolved to rects.
type page struct {
	shots [][]byte
	steps []Step
}

// renderPages renders every html scene, in parallel tabs of one
// browser; none starts no browser.
func (b *builder) renderPages(ctx context.Context) error {
	scenes := b.script.Scenes
	b.pages = make([]*page, len(scenes))
	if !slices.ContainsFunc(scenes, func(s Scene) bool { return s.HTML != "" }) {
		return nil
	}

	r, err := startRenderer(ctx)
	if err != nil {
		return err
	}
	defer r.close()

	errs := make([]error, len(scenes))
	limit := make(chan struct{}, 4)
	var wg sync.WaitGroup

	for i, s := range scenes {
		if s.HTML == "" {
			continue
		}
		wg.Go(func() {
			limit <- struct{}{}
			defer func() { <-limit }()

			b.pages[i], errs[i] = renderPage(ctx, r, s)
			if errs[i] != nil {
				errs[i] = fmt.Errorf("scene %d: %w", s.num, errs[i])
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// renderPage renders scene s's page: once with everything shown, or
// once per step with its data-step, its regions resolved. Warns of
// page errors, and of builds its steps don't reach.
func renderPage(ctx context.Context, r *renderer, s Scene) (*page, error) {
	t, err := r.open(ctx, s.HTML)
	if err != nil {
		return nil, err
	}
	defer t.close()

	where := fmt.Sprintf("scene %d", s.num)
	pg := &page{}
	if s.Steps == nil {
		snap, err := t.snap(ctx, 0)
		if err != nil {
			return nil, err
		}
		warnPage(os.Stderr, where, snap, 0)
		pg.shots = [][]byte{snap.png}
		t.warnErrors(os.Stderr, where)
		return pg, nil
	}

	for k, st := range s.Steps {
		st.Box, st.Zoom = st.Box.clone(), st.Zoom.clone()
		snap, err := t.snap(ctx, k+1, st.Box, st.Zoom)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", k+1, err)
		}
		if k == 0 {
			warnPage(os.Stderr, where, snap, len(s.Steps))
		}
		pg.shots = append(pg.shots, snap.png)
		pg.steps = append(pg.steps, st)
	}
	t.warnErrors(os.Stderr, where)
	return pg, nil
}

// warnPage warns of a page wider than the frame (cut at its edge),
// and of data-from / data-to steps a scene of steps steps misses.
func warnPage(w io.Writer, where string, s snapshot, steps int) {
	if s.width > frameW {
		fmt.Fprintf(w, "demo: %s: the page is %d px wide: the frame shows "+
			"%d\n", where, s.width, frameW)
	}
	switch {
	case s.last > 0 && steps == 0:
		fmt.Fprintf(w, "demo: %s: the page builds in %d steps "+
			"(data-from, data-to), the scene has none: all shown\n", where,
			s.last)
	case s.last > steps:
		fmt.Fprintf(w, "demo: %s: the page builds in %d steps "+
			"(data-from, data-to), the scene has %d\n", where, s.last, steps)
	}
}

// pageShots shows a rendered page as an image is shown (pictureShots):
// each step's own screenshot, its regions already rects. A screenshot
// like the one before is decoded once.
func pageShots(pg *page, s Scene, n int, cuts []int) (shots, []rect,
	error) {

	pics := make([]*picture, len(pg.shots))
	for k, data := range pg.shots {
		if k > 0 && bytes.Equal(data, pg.shots[k-1]) {
			pics[k] = pics[k-1]
			continue
		}

		var err error
		if pics[k], err = pngPicture(s.HTML, data); err != nil {
			return nil, nil, err
		}
	}

	if s.Steps == nil {
		return still(pics[0].canvas, nil, n), nil, nil
	}
	return pictureShots(func(k int) *picture { return pics[k] }, pg.steps,
		n, cuts)
}

// ---- Helpers ----

// pngPicture is a screenshot of the page at path, as a picture.
func pngPicture(path string, data []byte) (*picture, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: screenshot: %w", path, err)
	}
	return fitPicture(path, img)
}

// jsString is v as a JavaScript literal (JSON is one).
func jsString(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err) // strings and string slices always marshal
	}
	return string(data)
}

// clampRect is r [x, y, w, h] cut to a w x h page; nil if nothing's
// left.
func clampRect(r []int, w, h int) []int {
	x0, y0 := max(r[0], 0), max(r[1], 0)
	x1, y1 := min(r[0]+r[2], w), min(r[1]+r[3], h)
	if x1 <= x0 || y1 <= y0 {
		return nil
	}
	return []int{x0, y0, x1 - x0, y1 - y0}
}

// ---- demo html ----

// cmdHTML renders a page as an html scene shows it, at one step,
// optionally with a box spotlit, to a PNG; first it lists the page's
// size, its build steps and its ids with their boxes: what steps can
// point at.
func cmdHTML(args []string) error {
	fs := flag.NewFlagSet("html", flag.ExitOnError)
	out := fs.StringP("out", "o", "", "PNG to write (default "+
		"demo.scratch/html-<time>.png)")
	step := fs.Int("step", 0, "the step to show: data-step, and "+
		"data-from / data-to (default none: all shown)")
	box := fs.String("box", "", "spotlight a `selector`, or x,y,w,h")

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: demo html [flags] FILE\n")
		fs.PrintDefaults()
		fmt.Fprint(fs.Output(), htmlHelp)
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	path := fs.Arg(0)

	var region *Region
	if *box != "" {
		region = parseRegion(*box)
		if err := (Scene{HTML: path}).checkRegion(region); err != nil {
			return fmt.Errorf("--box: %w", err)
		}
	}

	ctx, stop := signalContext()
	defer stop()

	r, err := startRenderer(ctx)
	if err != nil {
		return err
	}
	defer r.close()

	t, err := r.open(ctx, path)
	if err != nil {
		return err
	}
	defer t.close()

	snap, err := t.snap(ctx, *step, region)
	if err != nil {
		return err
	}
	printPage(snap)
	warnPage(os.Stderr, path, snap, max(*step, snap.last))
	t.warnErrors(os.Stderr, path)

	pic, err := pngPicture(path, snap.png)
	if err != nil {
		return err
	}
	img := pic.canvas
	if region != nil {
		a, err := pic.aim(Step{Box: region})
		if err != nil {
			return err
		}
		img = pic.point(a).img
	}

	if *out == "" {
		dir, err := scratchDir()
		if err != nil {
			return err
		}
		*out = filepath.Join(dir, "html-"+time.Now().Format("150405")+".png")
	}
	if err := savePNG(img, *out); err != nil {
		return err
	}
	fmt.Println(*out)
	return nil
}

// htmlHelp is what an html scene's page can use: the reference agents
// read (demo build -h points here).
const htmlHelp = `
A page is plain HTML, 1280 px wide; a frame is 960 tall (100vh), a
taller page pages through. Assets load beside it or below. Under the
page's own styles (they always win), built-ins:
  palette   var(--bg --panel --line --text --dim --accent --blue
            --green --red); font: JetBrains Mono, 28 px (var(--mono))
  .center   the content in the middle of the frame
  .flow     side by side, centered; .flow.down: top to bottom
  .arrow    between boxes in a .flow: an arrow, a child is its label
  .grid     columns: style="--cols: 4" (default 3)
  .stack    under each other
  .card     a box; .card.accent / .blue / .add / .del: its border
  .tag      a small rounded label
  .dim .accent .blue .add .del  text colors; .big: a huge number
Steps: each renders with <html data-step="k">; an element with
data-from="2" shows from step 2, data-to="3" up to step 3 (hidden,
its space kept). A step's box and zoom take a CSS selector.
Async content (charts, mermaid): set window.demoReady to a promise;
the screenshot waits for it.
`

// parseRegion reads --box: x,y,w,h, else a selector.
func parseRegion(s string) *Region {
	var r []int
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			return &Region{Selector: s}
		}
		r = append(r, n)
	}
	return &Region{Rect: r}
}

// printPage lists what a page snapshot holds: size, build steps, ids.
func printPage(s snapshot) {
	fmt.Printf("page %dx%d", min(s.width, frameW), s.height)
	if s.last > 0 {
		fmt.Printf(", builds in %d steps (data-from, data-to)", s.last)
	}
	fmt.Println()

	for _, id := range s.ids {
		fmt.Printf("  #%-20s [%d, %d, %d, %d]\n", id.ID, id.Rect[0],
			id.Rect[1], id.Rect[2], id.Rect[3])
	}
}
