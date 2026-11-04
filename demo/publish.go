package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The PR body's demo block sits between these; a new demo replaces it
const (
	demoOpen  = "<!-- demo -->"
	demoClose = "<!-- /demo -->"
)

// cmdPublish links a built demo in a PR's body: the tailnet URL, its
// length and chapters.
func cmdPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	pr := fs.String("pr", "", "PR number or URL (default: this branch's)")

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: demo publish [-pr N] video.mp4\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}

	ctx := context.Background()
	video, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}

	url, err := publicURL(ctx, video)
	if err != nil {
		return err
	}

	chs, length, err := probeChapters(ctx, video)
	if err != nil {
		return err
	}

	// ---- Edit the body ----

	view := []string{"pr", "view", "--json", "number,body,url"}
	if *pr != "" {
		view = append(view, *pr)
	}

	out, err := exec.CommandContext(ctx, "gh", view...).Output()
	if err != nil {
		return fmt.Errorf("gh pr view: %w", err)
	}

	var p struct {
		Number int
		Body   string
		URL    string
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return err
	}

	body := withDemo(p.Body, demoBlock(url, length, chs))

	edit := exec.CommandContext(ctx, "gh", "pr", "edit",
		fmt.Sprint(p.Number), "--body-file", "-")
	edit.Stdin = strings.NewReader(body)
	edit.Stderr = os.Stderr
	if err := edit.Run(); err != nil {
		return fmt.Errorf("gh pr edit: %w", err)
	}

	fmt.Println(p.URL)
	return nil
}

// demoBlock renders the PR body's demo section.
func demoBlock(url string, length float64, chs []Chapter) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\n### 🎬 [Demo video](%s) (%s, tailnet)\n",
		demoOpen, url, clock(length))

	if len(chs) > 1 {
		b.WriteString("\n")
		for _, c := range chs {
			fmt.Fprintf(&b, "- `%s` %s\n", clock(c.Start), c.Title)
		}
	}

	b.WriteString(demoClose)
	return b.String()
}

// withDemo puts block in body: in place of the old one, else on top,
// where it's seen first.
func withDemo(body, block string) string {
	start := strings.Index(body, demoOpen)
	end := strings.Index(body, demoClose)

	if start >= 0 && end > start {
		return body[:start] + block + body[end+len(demoClose):]
	}

	if body = strings.TrimSpace(body); body == "" {
		return block + "\n"
	}
	return block + "\n\n" + body
}
