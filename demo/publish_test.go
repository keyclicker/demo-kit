package main

import (
	"strings"
	"testing"
)

func TestDemoBlock(t *testing.T) {
	got := demoBlock("https://n/files/v.mp4", 72,
		[]Chapter{{"Why", 0, 10}, {"Code", 10, 72}})

	for _, part := range []string{demoOpen, "[Demo video](https://n/files/v.mp4)",
		"(1:12, tailnet)", "- `0:10` Code", demoClose} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing %q in\n%s", part, got)
		}
	}
}

func TestWithDemo(t *testing.T) {
	block := demoOpen + "\nnew\n" + demoClose

	// Replaced in place
	old := "Intro\n\n" + demoOpen + "\nold\n" + demoClose + "\n\nRest"
	if got := withDemo(old, block); got != "Intro\n\n"+block+"\n\nRest" {
		t.Fatalf("replace: %q", got)
	}

	// Else on top
	if got := withDemo("Body\n", block); got != block+"\n\nBody" {
		t.Fatalf("prepend: %q", got)
	}
	if got := withDemo("", block); got != block+"\n" {
		t.Fatalf("empty: %q", got)
	}
}
