package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	flag "github.com/spf13/pflag"
)

// A contact sheet: frames of a video, every few seconds, tiled with
// their times and chapters. One look shows every scene, for checking a
// build before sending it.

const (
	sheetCols  = 4
	tileW      = frameW / sheetCols
	tileLabelH = 26
	autoTiles  = 20 // frames on a sheet, unless --every
)

// cmdFrames writes a video's contact sheet and prints its path.
func cmdFrames(args []string) error {
	fs := flag.NewFlagSet("frames", flag.ExitOnError)
	every := fs.Float64("every", 0, "seconds between frames (default: "+
		"about 20 frames)")
	out := fs.StringP("out", "o", "", "PNG to write (default "+
		"~/tmp/demo/<demo>-frames.png)")

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: demo frames [flags] VIDEO\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	video := fs.Arg(0)

	ctx := context.Background()
	chs, length, err := probeChapters(ctx, video)
	if err != nil {
		return err
	}
	if length <= 0 {
		return fmt.Errorf("%s: no length", video)
	}

	step := *every
	if step <= 0 {
		step = math.Max(1, math.Ceil(length/autoTiles))
	}
	var times []float64
	for t := step / 2; t < length; t += step {
		times = append(times, t)
	}

	work, err := os.MkdirTemp("", "demo-frames-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	tiles, err := grabFrames(ctx, video, times, work)
	if err != nil {
		return err
	}

	path := *out
	if path == "" {
		if path, err = sheetPath(video); err != nil {
			return err
		}
	}
	if err := savePNG(contactSheet(tiles, times, chs), path); err != nil {
		return err
	}

	fmt.Println(path)
	return nil
}

// sheetPath is ~/tmp/demo/<the video's folder>-frames.png.
func sheetPath(video string) (string, error) {
	dir, err := scratchDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(video)
	if err != nil {
		return "", err
	}
	name := filepath.Base(filepath.Dir(abs)) + "-frames.png"
	return filepath.Join(dir, name), nil
}

// grabFrames decodes the frames at times, tile-wide, in parallel.
func grabFrames(ctx context.Context, video string, times []float64,
	work string) ([]image.Image, error) {

	tiles := make([]image.Image, len(times))
	errs := make([]error, len(times))
	limit := make(chan struct{}, 6)
	var wg sync.WaitGroup

	for k, t := range times {
		wg.Go(func() {
			limit <- struct{}{}
			defer func() { <-limit }()

			png := filepath.Join(work, fmt.Sprintf("%03d.png", k))
			err := ffmpeg(ctx, "", nil, "-ss", ftoa(t), "-i", video,
				"-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:-2", tileW), png)
			if err == nil {
				tiles[k], err = loadImage(png)
			}
			errs[k] = err
		})
	}
	wg.Wait()
	return tiles, errors.Join(errs...)
}

// contactSheet tiles frames, sheetCols wide, each over its time and
// chapter.
func contactSheet(tiles []image.Image, times []float64,
	chs []Chapter) *image.RGBA {

	tileH := tiles[0].Bounds().Dy() + tileLabelH
	rows := (len(tiles) + sheetCols - 1) / sheetCols
	img := image.NewRGBA(image.Rect(0, 0, frameW, rows*tileH))
	fill(img, 0, 0, frameW, rows*tileH, colBG)
	face := monoFace(false, 15)

	for k, tile := range tiles {
		x, y := k%sheetCols*tileW, k/sheetCols*tileH
		draw.Draw(img, tile.Bounds().Add(image.Pt(x, y)), tile,
			tile.Bounds().Min, draw.Src)

		label := clock(times[k])
		if c := chapterAt(chs, times[k]); c != "" {
			label += "  " + c
		}
		label = clip(label, (tileW-12)/textWidth(face, "M"))
		drawText(img, face, x+6, y+tileH-tileLabelH, tileLabelH, label,
			colDim)
	}
	return img
}

// chapterAt names the chapter playing at t.
func chapterAt(chs []Chapter, t float64) string {
	for _, c := range chs {
		if t >= c.Start && t < c.End {
			return strings.TrimSpace(c.Title)
		}
	}
	return ""
}
