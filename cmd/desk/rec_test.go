package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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

// part makes a 1 s test video at dir/name, with sound as long as its
// picture if sound is set; broken cuts it short.
func part(t *testing.T, dir, name string, sound, broken bool) string {
	t.Helper()

	path := filepath.Join(dir, name)
	args := []string{"-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=30:duration=1"}
	if sound {
		args = append(args, "-f", "lavfi", "-i",
			"sine=frequency=440:sample_rate=48000:duration=1", "-c:a", "aac")
	}
	args = append(args, "-c:v", "libx264", "-movflags", "+faststart", path)

	out, err := exec.Command("ffmpeg", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}

	if broken {
		if err := os.Truncate(path, 3000); err != nil {
			t.Fatal(err)
		}
	}

	return path
}

// A join is all or nothing: the video appears only whole, and a broken
// part keeps every part and no video.
func TestJoinSegments(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}

	for _, tc := range []struct {
		name   string
		sound  bool
		broken []bool
		ok     bool
	}{
		{"whole", false, []bool{false, false, false}, true},
		{"whole, sound", true, []bool{false, false, false}, true},
		{"one", false, []bool{false}, true},
		{"broken middle", false, []bool{false, true, false}, false},
		{"broken middle, sound", true, []bool{false, true, false}, false},
		{"broken alone", false, []bool{true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			video := filepath.Join(dir, "v.mp4")

			var parts []string
			for k, broken := range tc.broken {
				parts = append(parts, part(t, dir, filepath.Base(
					partPath(video, k+1)), tc.sound, broken))
			}

			err := joinSegments(video, parts)
			if (err == nil) != tc.ok {
				t.Fatalf("joinSegments: %v; want ok %v", err, tc.ok)
			}

			left, _ := os.ReadDir(dir)
			var names []string
			for _, e := range left {
				names = append(names, e.Name())
			}

			switch {
			case tc.ok && (len(names) != 1 || names[0] != "v.mp4"):
				t.Errorf("left %q; want just v.mp4", names)
			case !tc.ok && (exists(video) || len(names) != len(parts)):
				t.Errorf("left %q; want only the %d parts", names,
					len(parts))
			}
		})
	}
}
