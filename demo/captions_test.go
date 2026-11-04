package main

import (
	"math"
	"strings"
	"testing"
)

func TestSplitSentences(t *testing.T) {
	got := splitSentences("One. Two?  Three!\nFour")
	want := []string{"One.", "Two?", "Three!", "Four"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

func TestSplitWords(t *testing.T) {
	s := strings.Repeat("word ", 30) // 149 chars
	for _, p := range splitWords(strings.TrimSpace(s), 80) {
		if len(p) > 80 {
			t.Fatalf("piece over 80: %d", len(p))
		}
	}
	if got := splitWords("short", 80); len(got) != 1 {
		t.Fatalf("got %q", got)
	}
}

func TestNarrationCuesCoverSpeech(t *testing.T) {
	cues := narrationCues("A short one. Then a much longer sentence here.", 2, 4)
	if len(cues) != 2 {
		t.Fatalf("got %d cues", len(cues))
	}
	if cues[0].Start != 2 || math.Abs(cues[1].End-6) > 1e-9 {
		t.Fatalf("cues span %v..%v, want 2..6", cues[0].Start, cues[1].End)
	}
	if cues[0].End != cues[1].Start || cues[0].End-2 >= cues[1].End-cues[1].Start {
		t.Fatalf("cue times not by length: %+v", cues)
	}
}

func TestSRT(t *testing.T) {
	got := srt([]cue{{61.5, 3723.0405, "hi"}})
	want := "1\n00:01:01,500 --> 01:02:03,041\nhi\n\n"
	if got != want {
		t.Fatalf("srt = %q", got)
	}
}

func TestChaptersGroupScenes(t *testing.T) {
	scenes := []Scene{{}, {Chapter: "UI"}, {}, {Chapter: "Code"}}
	got := chapters(scenes, []float64{1, 2, 3, 4}, "Intro")

	want := []Chapter{{"Intro", 0, 1}, {"UI", 1, 6}, {"Code", 6, 10}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chapter %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFFMetadata(t *testing.T) {
	got := ffmetadata("a=b", []Chapter{{"x;y", 0, 1.5}})
	for _, part := range []string{";FFMETADATA1\n", "title=a\\=b\n",
		"START=0\nEND=1500\ntitle=x\\;y\n"} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing %q in\n%s", part, got)
		}
	}
}

// Widths are in characters: Cyrillic is two bytes each
func TestSplitWordsRunes(t *testing.T) {
	s := strings.TrimSpace(strings.Repeat("слово ", 12)) // 71 runes
	if got := splitWords(s, 80); len(got) != 1 {
		t.Fatalf("71 runes split in %d", len(got))
	}
}
