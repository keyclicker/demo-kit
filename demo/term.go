package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// A term scene is a terminal without the screen recording: commands
// run for real, once, in one shell on a pty (so tools color their
// output), then the typing and output are drawn frame by frame. It
// takes as long as the commands do, not as long as watching them.
// Scenes' sessions run one after another, in script order, before any
// scene is drawn: a later one may use what an earlier one made.

// TermSpec is a terminal session: Run's commands, typed and run in
// turn in one shell in Dir (default: the current directory).
type TermSpec struct {
	Run []string `json:"run"`
	Dir string   `json:"dir,omitempty"`
}

// check rejects sessions with nothing to run, and steps that don't
// pair with the commands one to one.
func (t TermSpec) check(steps int) error {
	switch {
	case len(t.Run) == 0:
		return errors.New("term: needs run: [commands]")
	case steps > 0 && steps != len(t.Run):
		return fmt.Errorf("term: %d commands, %d steps: one step per "+
			"command, or say for all", len(t.Run), steps)
	}
	for i, c := range t.Run {
		if strings.TrimSpace(c) == "" {
			return fmt.Errorf("term: command %d is empty", i+1)
		}
	}
	return nil
}

// The terminal: code's font and sizes, under a header bar. Commands
// run termCols wide, the default size's; a session whose screen fits
// a larger size unwrapped is drawn at it
const (
	termTimeout = 2 * time.Minute

	typeCPS = 30 // typing speed, chars/s, when there's time for it
)

var termCols = termColsAt(unifiedMetrics)

// termColsAt is how many columns a terminal at m's size shows.
func termColsAt(m metrics) int {
	return (frameW - 2*margin) / int(math.Round(0.6*m.fontSize))
}

// termRowsAt is how many rows a terminal at m's size shows.
func termRowsAt(m metrics) int {
	return (frameH - fileRowH - 2*margin) / m.rowH
}

// ---- Running ----

// termResult is one command's output, raw (ANSI), and exit status.
type termResult struct {
	Output string
	Status int
}

// markers delimit commands in a session's output. Random per run, so
// no command's own output can fake one.
type markers struct{ start, end string }

// newMarkers makes a run's markers: a control byte and a random token.
func newMarkers() markers {
	token := rand.Text()
	return markers{start: "\x1e" + token, end: "\x1f" + token}
}

// runTerm runs t's commands in one bash on a pty (script(1)), stdin
// closed and pagers off, and splits the output per command.
func runTerm(ctx context.Context, t TermSpec, scriptPath string) (
	[]termResult, error) {

	mark := newMarkers()
	var sh strings.Builder
	fmt.Fprintf(&sh, "stty cols %d rows %d 2>/dev/null\n", termCols,
		termRowsAt(unifiedMetrics))
	for _, c := range t.Run {
		fmt.Fprintf(&sh, "printf '%s'\n{ %s\n} </dev/null\n"+
			"printf '%s%%d\\n' $?\n", mark.start, c, mark.end)
	}
	if err := os.WriteFile(scriptPath, []byte(sh.String()), 0o644); err != nil {
		return nil, err
	}

	timed, cancel := context.WithTimeout(ctx, termTimeout)
	defer cancel()

	cmd := exec.CommandContext(timed, "script", "--quiet", "--echo",
		"never", "--command", "bash --noprofile --norc "+
			shellQuote(scriptPath), "/dev/null")
	cmd.Dir = t.Dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "PAGER=cat",
		"GIT_PAGER=cat", "COLUMNS="+strconv.Itoa(termCols))

	// Its own process group, killed whole: script, and with its pty
	// gone, the session's bash and commands (they get SIGHUP)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	out, err := cmd.Output()
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("term: %w", context.Cause(ctx))
	case timed.Err() != nil:
		return nil, fmt.Errorf("term: still running after %s", termTimeout)
	case err != nil:
		return nil, fmt.Errorf("term: script: %w", err)
	}
	return splitSession(string(out), len(t.Run), mark)
}

// splitSession cuts a session's output at its markers into n results.
func splitSession(out string, n int, mark markers) ([]termResult,
	error) {

	parts := strings.Split(out, mark.start)
	if len(parts) != n+1 {
		return nil, fmt.Errorf("term: %d of %d commands ran", len(parts)-1, n)
	}

	var results []termResult
	for _, p := range parts[1:] {
		body, status, found := strings.Cut(p, mark.end)
		if !found {
			return nil, errors.New("term: a command ended the shell")
		}
		code, _ := strconv.Atoi(strings.TrimSpace(status))
		results = append(results, termResult{body, code})
	}
	return results, nil
}

// shellQuote quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---- ANSI ----

// The 16 colors, GitHub Dark Default's (the demo Ghostty's theme)
var ansiColors = [16]color.RGBA{
	rgb(0x484f58), rgb(0xff7b72), rgb(0x3fb950), rgb(0xd29922),
	rgb(0x58a6ff), rgb(0xbc8cff), rgb(0x39c5cf), rgb(0xb1bac4),
	rgb(0x6e7681), rgb(0xffa198), rgb(0x56d364), rgb(0xe3b341),
	rgb(0x79c0ff), rgb(0xd2a8ff), rgb(0x56d4dd), rgb(0xffffff),
}

// parseANSI reads terminal output into lines of colored runs: SGR
// colors and bold are kept, other escapes dropped, \r's before a \n
// are part of it (CRLF), a lone \r starts its line over (progress
// bars), tabs expand to 8.
func parseANSI(s string) [][]run {
	var lines [][]run
	var line []run
	style := run{col: colText}
	cols := 0

	put := func(text string) {
		if n := len(line); n > 0 && line[n-1].col == style.col &&
			line[n-1].bold == style.bold {
			line[n-1].text += text
		} else {
			r := style
			r.text = text
			line = append(line, r)
		}
		cols += utf8.RuneCountInString(text)
	}

	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			n, sgr := escape(s[i:])
			if sgr != nil {
				style = applySGR(style, sgr)
			}
			i += n
			continue

		case c == '\n':
			lines, line, cols = append(lines, line), nil, 0
		case c == '\r':
			// \r\n ends a line, as does \r\r\n: a program's own CRLF
			// after the pty's onlcr. A lone \r starts the line over
			j := i
			for j < len(s) && s[j] == '\r' {
				j++
			}
			if j < len(s) && s[j] != '\n' {
				line, cols = nil, 0
			}
			i = j
			continue
		case c == '\t':
			put(strings.Repeat(" ", 8-cols%8))
		case c < 0x20 || c == 0x7f:
			// bells, backspaces: nothing to draw

		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			put(string(r))
			i += size
			continue
		}
		i++
	}

	if line != nil {
		lines = append(lines, line)
	}
	return lines
}

// escape measures the escape sequence at s's start; for SGR (ESC [ ...
// m) it also returns the parameters.
func escape(s string) (n int, sgr []int) {
	if len(s) < 2 {
		return len(s), nil
	}

	switch s[1] {
	case '[': // CSI: parameters, then a final byte in @..~
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				if s[i] == 'm' {
					return i + 1, sgrParams(s[2:i])
				}
				return i + 1, nil
			}
		}
		return len(s), nil

	case ']': // OSC: until BEL or ESC \
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1, nil
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2, nil
			}
		}
		return len(s), nil
	}
	return 2, nil
}

// sgrParams parses "1;38;5;208"; empty means 0 (reset).
func sgrParams(s string) []int {
	var out []int
	for _, f := range strings.Split(s, ";") {
		n, _ := strconv.Atoi(f)
		out = append(out, n)
	}
	return out
}

// applySGR updates a style by SGR parameters: reset, bold, and the
// foreground in 16, 256 or 24-bit color. Backgrounds are skipped.
func applySGR(style run, p []int) run {
	for i := 0; i < len(p); i++ {
		switch n := p[i]; {
		case n == 0:
			style = run{col: colText}
		case n == 1:
			style.bold = true
		case n == 22:
			style.bold = false
		case n >= 30 && n <= 37:
			style.col = ansiColors[n-30]
		case n >= 90 && n <= 97:
			style.col = ansiColors[n-90+8]
		case n == 39:
			style.col = colText

		case n == 38 || n == 48: // extended: 5;N or 2;R;G;B
			var col color.Color
			switch {
			case i+2 < len(p) && p[i+1] == 5:
				col, i = color256(p[i+2]), i+2
			case i+4 < len(p) && p[i+1] == 2:
				col = color.RGBA{uint8(p[i+2]), uint8(p[i+3]),
					uint8(p[i+4]), 0xff}
				i += 4
			}
			if n == 38 && col != nil {
				style.col = col
			}
		}
	}
	return style
}

// color256 is xterm's 256-color palette: the 16, a 6x6x6 cube, grays.
func color256(n int) color.Color {
	switch {
	case n < 16:
		return ansiColors[max(n, 0)]
	case n < 232:
		n -= 16
		level := func(v int) uint8 {
			if v == 0 {
				return 0
			}
			return uint8(55 + 40*v)
		}
		return color.RGBA{level(n / 36), level(n / 6 % 6), level(n % 6), 0xff}
	default:
		g := uint8(8 + 10*(min(n, 255)-232))
		return color.RGBA{g, g, g, 0xff}
	}
}

// ---- Drawing ----

// session is a term scene's run: where, and each command's result.
type session struct {
	dir     string
	results []termResult
}

// runSessions runs every term scene's commands, one scene after
// another in script order (a scene may use what an earlier one made),
// and keeps them for termShots.
func (b *builder) runSessions(ctx context.Context) error {
	b.sessions = make([]session, len(b.script.Scenes))
	for i, s := range b.script.Scenes {
		if s.Term == nil {
			continue
		}

		sess, err := runSession(ctx, s, b.scenePath(i, "term.sh"))
		if err != nil {
			return fmt.Errorf("scene %d: %w", s.num, err)
		}
		b.sessions[i] = sess
	}
	return nil
}

// runSession runs scene s's commands in its directory, warning about
// the ones that fail.
func runSession(ctx context.Context, s Scene, scriptPath string) (session,
	error) {

	dir, err := filepath.Abs(orDefault(s.Term.Dir, "."))
	if err != nil {
		return session{}, err
	}
	spec := *s.Term
	spec.Dir = dir

	results, err := runTerm(ctx, spec, scriptPath)
	if err != nil {
		return session{}, err
	}
	for k, r := range results {
		if r.Status != 0 {
			fmt.Fprintf(os.Stderr, "demo: scene %d: %q exited %d\n", s.num,
				spec.Run[k], r.Status)
		}
	}
	return session{dir, results}, nil
}

// termShots plays scene i's session back: each command typed and its
// output shown within its step (all of them in turn when the scene
// just says).
func (b *builder) termShots(i int, s Scene, n int, cuts []int) shots {
	sess := b.sessions[i]
	results := sess.results

	// Which commands play in which slot of frames
	type slot struct{ from, to, start, end int }
	var slots []slot
	if s.Steps == nil {
		slots = []slot{{0, len(results), 0, n}}
	} else {
		for k := range results {
			end := n
			if k+1 < len(cuts) {
				end = cuts[k+1]
			}
			slots = append(slots, slot{k, k + 1, cuts[k], end})
		}
	}

	screen := newScreen(sess.dir)
	screen.fit(s.Term.Run, results)
	return func(emit func(shot) error) error {
		for _, sl := range slots {
			count := sl.to - sl.from
			for k := sl.from; k < sl.to; k++ {
				j := k - sl.from
				from := sl.start + (sl.end-sl.start)*j/count
				to := sl.start + (sl.end-sl.start)*(j+1)/count

				err := screen.play(s.Term.Run[k], results[k].Output,
					to-from, emit)
				if err != nil {
					return err
				}
			}
		}
		return nil
	}
}

// orDefault is s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// screen is the terminal being played back: finished lines, and the
// prompt the next command is typed at; drawn at size, its lines right
// under the header bar, as a real terminal fills from the top.
type screen struct {
	header string
	prompt []run
	lines  [][]run
	size   metrics
}

// newScreen is an empty terminal in dir, its prompt as the demo zsh's
// (skills/demo/zsh): the directory's name, then $.
func newScreen(dir string) *screen {
	name := filepath.Base(dir)
	if home, err := os.UserHomeDir(); err == nil && dir == home {
		name = "~"
	}
	return &screen{
		header: homePath(dir),
		prompt: []run{{text: name, col: ansiColors[4]}, {text: " "},
			{text: "$", col: ansiColors[2]}, {text: " "}},
		size: unifiedMetrics,
	}
}

// fit sizes the screen to the session's last picture, every command
// and its output: the largest size that shows it all and wraps no more
// than the default. One size for the whole scene, so nothing jumps
// while it plays.
func (sc *screen) fit(run []string, results []termResult) {
	final := &screen{prompt: sc.prompt}
	for k, r := range results {
		final.enter(commandRuns(run[k]), r.Output)
	}
	lines := append(final.lines, final.promptLines(nil)...)

	rows := func(m metrics) (n, wraps int) {
		for _, l := range lines {
			k := len(wrapTokens(l, termColsAt(m)))
			n, wraps = n+k, wraps+k-1
		}
		return n, wraps
	}

	_, wraps := rows(unifiedMetrics)
	for _, m := range largeMetrics {
		if n, w := rows(m); n <= termRowsAt(m) && w <= wraps {
			sc.size = m
			return
		}
	}
}

// play shows one command over n frames: a beat at the prompt, the
// command typed out, enter, then its output under it until the end.
func (sc *screen) play(command, output string, n int,
	emit func(shot) error) error {

	typed := commandRuns(command)
	chars := utf8.RuneCountInString(command)

	pre := min(frameCount(0.3), n/6)
	typing := min(max(1, chars*fps/typeCPS), max(1, n*2/5))
	enter := min(frameCount(0.15), n/10)
	rest := max(0, n-pre-typing-enter)

	// ---- At the prompt, typing ----

	if err := emit(sc.shot(nil, pre)); err != nil {
		return err
	}
	for f := range typing {
		shown := prefixRuns(typed, (chars*(f+1)+typing-1)/typing)
		if err := emit(sc.shot(shown, 1)); err != nil {
			return err
		}
	}
	if err := emit(sc.shot(typed, enter)); err != nil {
		return err
	}

	sc.enter(typed, output)
	return emit(sc.shot(nil, rest))
}

// enter moves a typed command and its output into history.
func (sc *screen) enter(typed [][]run, output string) {
	sc.lines = append(sc.lines, sc.promptLines(typed)...)
	if strings.TrimSpace(output) != "" {
		out := parseANSI(strings.TrimRight(output, "\r\n"))
		sc.lines = append(sc.lines, out...)
	}
}

// promptLines is a typed command at the prompt, a line per line of it,
// continuations after "> ".
func (sc *screen) promptLines(cmd [][]run) [][]run {
	var out [][]run
	for i, l := range cmd {
		head := sc.prompt
		if i > 0 {
			head = []run{{text: "> ", col: colDim}}
		}
		out = append(out, append(append([]run{}, head...), l...))
	}
	if len(out) == 0 {
		out = [][]run{append([]run{}, sc.prompt...)}
	}
	return out
}

// shot draws the screen with typed at the prompt, the cursor after it,
// for n frames.
func (sc *screen) shot(typed [][]run, n int) shot {
	lines := append(append([][]run{}, sc.lines...), sc.promptLines(typed)...)
	return shot{img: sc.draw(lines), n: n}
}

// commandRuns colors a command as the shell would see it, a []run per
// line.
func commandRuns(cmd string) [][]run {
	lexer := chroma.Coalesce(lexers.Get("bash"))
	return highlightLines(lexer, strings.Split(cmd, "\n"))
}

// prefixRuns cuts lines of runs to their first n runes, a line break
// counting as one.
func prefixRuns(lines [][]run, n int) [][]run {
	var out [][]run
	for i, l := range lines {
		if i > 0 {
			if n == 0 {
				break
			}
			n--
		}

		var cut []run
		for _, r := range l {
			if n == 0 {
				break
			}
			runes := []rune(r.text)
			k := min(n, len(runes))
			piece := r
			piece.text = string(runes[:k])
			cut, n = append(cut, piece), n-k
		}
		out = append(out, cut)
	}
	return out
}

// draw draws the terminal with lines on it: a header bar with the
// directory, then the last lines that fit, wrapped, and a block cursor
// at the end.
func (sc *screen) draw(lines [][]run) *image.RGBA {
	m := sc.size
	img := newCanvas(frameH)
	regular, bold := monoFace(false, m.fontSize), monoFace(true, m.fontSize)
	adv := textWidth(regular, "M")

	fill(img, 0, 0, frameW, fileRowH, colFileBG)
	x := drawText(img, bold, margin, 0, fileRowH, sc.header, colText)
	drawText(img, regular, x, 0, fileRowH, "  terminal", colDim)

	var rows [][]run
	for _, l := range lines {
		rows = append(rows, wrapTokens(l, termColsAt(m))...)
	}
	rows = rows[max(0, len(rows)-termRowsAt(m)):]

	y := fileRowH + margin
	end := margin
	for _, row := range rows {
		x := margin
		for _, r := range row {
			face := regular
			if r.bold {
				face = bold
			}
			x = drawText(img, face, x, y, m.rowH, r.text, r.col)
		}
		end = x
		y += m.rowH
	}

	// The cursor, after the last line's text
	fill(img, end, y-m.rowH+3, adv, m.rowH-6, colText)
	return img
}
