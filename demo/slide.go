package main

import (
	"errors"
	"image"
	"image/color"
	"strings"

	"golang.org/x/image/font"
)

// Slides are big centered text: a title card, a callout (one number
// or sentence, huge), an end card. Anything longer is a markdown file
// shown as code, with steps.

// SlideSpec is a slide: one of Title, Callout or End sets its kind and
// headline; Text is a line under it, Lines a list under that.
type SlideSpec struct {
	Title   string   `json:"title,omitempty"`
	Callout string   `json:"callout,omitempty"`
	End     string   `json:"end,omitempty"`
	Text    string   `json:"text,omitempty"`
	Lines   []string `json:"lines,omitempty"`
}

// check rejects slides with no headline or two, and ones that don't
// fit a frame.
func (s SlideSpec) check() error {
	if btoi(s.Title != "")+btoi(s.Callout != "")+btoi(s.End != "") != 1 {
		return errors.New("slide: needs exactly one of title, callout, end")
	}
	if slideHeight(s.blocks()) > frameH-2*slidePad {
		return errors.New("slide: too much text for a frame: show a " +
			"markdown file as code instead")
	}
	return nil
}

const (
	slidePad   = 96 // around the text
	slideWidth = frameW - 2*slidePad
	blockGap   = 36 // between headline, text and lines
)

// textBlock is a run of wrapped lines in one style.
type textBlock struct {
	lines []string
	face  font.Face
	rowH  int
	col   color.Color
}

// blocks lays the slide's text out: headline, text, lines.
func (s SlideSpec) blocks() []textBlock {
	head, size, col := s.Title, 60.0, color.Color(colText)
	switch {
	case s.Callout != "":
		head, size, col = s.Callout, 110, colFocus
	case s.End != "":
		head, size = s.End, 55
	}

	out := []textBlock{newBlock([]string{head}, true, size, col)}
	if s.Text != "" {
		out = append(out, newBlock([]string{s.Text}, false, 30, colDim))
	}
	if s.Lines != nil {
		out = append(out, newBlock(s.Lines, false, 25, colText))
	}
	return out
}

// newBlock wraps texts, each on lines of its own, to the slide's width
// at size px.
func newBlock(texts []string, bold bool, size float64,
	col color.Color) textBlock {

	face := monoFace(bold, size)
	cols := slideWidth / textWidth(face, "M")

	var lines []string
	for _, t := range texts {
		lines = append(lines, splitWords(strings.TrimSpace(t), cols)...)
	}
	return textBlock{lines: lines, face: face, rowH: int(size * 1.35),
		col: col}
}

// slideHeight is the blocks' height, gaps included.
func slideHeight(blocks []textBlock) int {
	h := blockGap * (len(blocks) - 1)
	for _, b := range blocks {
		h += b.rowH * len(b.lines)
	}
	return h
}

// drawSlide draws s on a frame, every line centered, the whole block
// centered too.
func drawSlide(s SlideSpec) *image.RGBA {
	img := newCanvas(frameH)
	blocks := s.blocks()
	y := (frameH - slideHeight(blocks)) / 2

	for _, b := range blocks {
		for _, line := range b.lines {
			x := (frameW - textWidth(b.face, line)) / 2
			drawText(img, b.face, x, y, b.rowH, line, b.col)
			y += b.rowH
		}
		y += blockGap
	}
	return img
}
