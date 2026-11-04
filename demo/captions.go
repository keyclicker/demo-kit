package main

import (
	"fmt"
	"regexp"
	"strings"
)

// ---- Captions ----

// cue is one caption on screen, in seconds from the video's start.
type cue struct {
	Start, End float64
	Text       string
}

// maxCue keeps a caption to two lines at the burned-in font size.
const maxCue = 80

var sentenceEnd = regexp.MustCompile(`[.!?…]+["')\]]*\s+`)

// narrationCues splits text into caption-sized cues spread over the
// speech, each lasting in proportion to its length: TTS speaks at a
// roughly even rate, so that tracks the voice closely enough.
func narrationCues(text string, start, dur float64) []cue {
	var chunks []string
	for _, s := range splitSentences(text) {
		chunks = append(chunks, splitWords(s, maxCue)...)
	}

	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	if total == 0 {
		return nil
	}

	cues := make([]cue, 0, len(chunks))
	at := start
	for _, c := range chunks {
		d := dur * float64(len(c)) / float64(total)
		cues = append(cues, cue{at, at + d, c})
		at += d
	}
	return cues
}

// splitSentences cuts text after . ! ? and …, keeping the marks.
func splitSentences(text string) []string {
	// The trailing space lets the last sentence's mark match too
	text = strings.Join(strings.Fields(text), " ") + " "

	var out []string
	for {
		loc := sentenceEnd.FindStringIndex(text)
		if loc == nil {
			break
		}
		out = append(out, strings.TrimSpace(text[:loc[1]]))
		text = text[loc[1]:]
	}

	if rest := strings.TrimSpace(text); rest != "" {
		out = append(out, rest)
	}
	return out
}

// splitWords cuts s into pieces of at most n bytes at spaces, as even
// as it can, so a long sentence becomes balanced cues.
func splitWords(s string, n int) []string {
	if len(s) <= n {
		return []string{s}
	}

	pieces := (len(s) + n - 1) / n
	target := len(s) / pieces

	var out []string
	var cur strings.Builder
	for _, w := range strings.Fields(s) {
		if cur.Len() > 0 && (cur.Len()+1+len(w) > n || cur.Len() >= target) {
			out = append(out, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(w)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// srt renders cues as a SubRip file.
func srt(cues []cue) string {
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1,
			srtTime(c.Start), srtTime(c.End), c.Text)
	}
	return b.String()
}

// srtTime formats seconds as HH:MM:SS,mmm.
func srtTime(secs float64) string {
	ms := int(secs*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d,%03d",
		ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// ---- Chapters ----

// ffmetadata renders a title and chapters as an FFMETADATA1 file.
func ffmetadata(title string, chapters []Chapter) string {
	var b strings.Builder
	b.WriteString(";FFMETADATA1\n")
	if title != "" {
		fmt.Fprintf(&b, "title=%s\n", metaEscape(title))
	}

	for _, c := range chapters {
		fmt.Fprintf(&b, "\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\n"+
			"title=%s\n", int(c.Start*1000+0.5), int(c.End*1000+0.5),
			metaEscape(c.Title))
	}
	return b.String()
}

// metaEscape backslash-escapes FFMETADATA's special characters.
func metaEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "=", `\=`, ";", `\;`, "#", `\#`,
		"\n", `\`+"\n")
	return r.Replace(s)
}

// chapters groups scenes into chapters: a scene with a chapter title
// starts one, a scene without continues the last. lengths are the
// scenes' durations.
func chapters(scenes []Scene, lengths []float64, fallback string) []Chapter {
	var out []Chapter
	at := 0.0

	for i, s := range scenes {
		if s.Chapter != "" || len(out) == 0 {
			title := s.Chapter
			if title == "" {
				title = fallback
			}
			out = append(out, Chapter{Title: title, Start: at})
		}

		at += lengths[i]
		out[len(out)-1].End = at
	}
	return out
}
