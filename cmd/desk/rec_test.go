package main

import "testing"

func TestRecFileRoundTrip(t *testing.T) {
	want := Recording{
		Video:  "/home/u/public/recordings/a b.mp4",
		Paused: true,
		Owner:  Owner{PID: 42, Start: 7, Name: "claude"},
	}

	got, ok := parseRec(recFile(want))
	if !ok || got != want {
		t.Fatalf("parseRec(recFile(r)) = %+v, %v; want %+v", got, ok, want)
	}

	if _, ok := parseRec("# state: paused\n"); ok {
		t.Fatal("accepted a state file with no owner or video")
	}
}

func TestPartPath(t *testing.T) {
	got := partPath("/r/2026-11-04-desk-10.mp4", 2)
	if want := "/r/2026-11-04-desk-10.part2.mp4"; got != want {
		t.Fatalf("partPath = %q; want %q", got, want)
	}
}

// The concat demuxer quotes like a shell: ' closes, \' is a quote
func TestConcatList(t *testing.T) {
	got := concatList([]string{"/a/one.mp4", "/a/it's.mp4"})
	want := "file '/a/one.mp4'\nfile '/a/it'\\''s.mp4'\n"
	if got != want {
		t.Fatalf("concatList = %q; want %q", got, want)
	}
}
