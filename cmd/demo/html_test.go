package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestClampRect(t *testing.T) {
	cases := []struct{ in, want []int }{
		{[]int{10, 20, 30, 40}, []int{10, 20, 30, 40}},
		{[]int{-5, -5, 30, 40}, []int{0, 0, 25, 35}},     // off the top left
		{[]int{1270, 0, 30, 40}, []int{1270, 0, 10, 40}}, // past the edge
		{[]int{1300, 0, 30, 40}, nil},                    // all off
	}
	for _, c := range cases {
		if got := clampRect(c.in, frameW, frameH); !slices.Equal(got, c.want) {
			t.Errorf("clampRect(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseRegion(t *testing.T) {
	if r := parseRegion("1, 2,3,4"); !slices.Equal(r.Rect, []int{1, 2, 3, 4}) {
		t.Fatalf("numbers: %+v", r)
	}
	if r := parseRegion("#api > .db"); r.Selector != "#api > .db" {
		t.Fatalf("selector: %+v", r)
	}
}

func TestDevToolsURL(t *testing.T) {
	stderr := strings.NewReader("[1:2:ERROR:dbus] noise\n" +
		"DevTools listening on ws://127.0.0.1:4242/devtools/browser/x\n" +
		"more noise\n")
	url, err := devToolsURL(stderr, time.Second)
	if err != nil || url != "ws://127.0.0.1:4242/devtools/browser/x" {
		t.Fatalf("got %q, %v", url, err)
	}

	// Exits without it: the error quotes what it said
	_, err = devToolsURL(strings.NewReader("no display\n"), time.Second)
	if err == nil || !strings.Contains(err.Error(), "no display") {
		t.Fatalf("got %v", err)
	}
}
