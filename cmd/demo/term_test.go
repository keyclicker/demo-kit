package main

import (
	"slices"
	"testing"
)

func TestParseANSI(t *testing.T) {
	lines := parseANSI("\x1b]0;title\x07\x1b[1;32mok\x1b[0m done\r\n" +
		"10%\r50%\r\n\tx\x1b[38;5;196my")
	if len(lines) != 3 {
		t.Fatalf("%d lines: %+v", len(lines), lines)
	}

	ok := lines[0][0]
	if ok.text != "ok" || !ok.bold || ok.col != ansiColors[2] ||
		lines[0][1].text != " done" || lines[0][1].bold {
		t.Fatalf("styles: %+v", lines[0])
	}
	if lines[1][0].text != "50%" {
		t.Fatalf("\\r redraws its line: %+v", lines[1])
	}
	if lines[2][0].text != "        x" || lines[2][1].col != color256(196) {
		t.Fatalf("tab, 256 colors: %+v", lines[2])
	}

	// A program's own CRLF reaches us as \r\r\n (the pty's onlcr)
	got := texts(parseANSI("HTTP/1.1 200 OK\r\r\nX: y\r\r\n"))
	if !slices.Equal(got, []string{"HTTP/1.1 200 OK", "X: y"}) {
		t.Fatalf("CRLF: %q", got)
	}
}

// texts is lines' plain text, styles dropped.
func texts(lines [][]run) []string {
	var out []string
	for _, l := range lines {
		var text string
		for _, r := range l {
			text += r.text
		}
		out = append(out, text)
	}
	return out
}

// Escapes leave no letters behind, whatever their kind
func TestParseANSIEscapes(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     []string
	}{
		{"sgr0 of xterm-256color", "test result: ok\x1b(B\x1b[m. 1 passed",
			[]string{"test result: ok. 1 passed"}},
		{"charset with intermediates", "a\x1b$)Bb\x1b#8c",
			[]string{"abc"}},
		{"keypad mode", "a\x1b=b\x1b>c", []string{"abc"}},
		{"DCS to ST", "a\x1bP1$r0m\x1b\\b", []string{"ab"}},
		{"APC, PM, SOS", "a\x1b_x\x1b\\b\x1b^y\x1b\\c\x1bXz\x1b\\d",
			[]string{"abcd"}},
		{"OSC to ST", "a\x1b]8;;http://x\x1b\\b", []string{"ab"}},
		{"cut off CSI", "ok\x1b[3", []string{"ok"}},
		{"cut off DCS", "ok\x1bPq#0", []string{"ok"}},
		{"lone ESC", "ok\x1b", []string{"ok"}},
		{"ESC before a newline", "a\x1b\nb", []string{"a", "b"}},
		{"column 1 redraws", "10%\x1b[G50%\x1b[1G90%\r\n",
			[]string{"90%"}},
		{"column 1 at a line's end", "done\x1b[G\r\nnext",
			[]string{"done", "next"}},
		{"other moves dropped", "a\x1b[5Gb\x1b[2Ac", []string{"abc"}},
	} {
		if got := texts(parseANSI(tc.in)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q; want %q", tc.name, got, tc.want)
		}
	}
}
