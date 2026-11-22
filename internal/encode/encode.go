// Package encode decides how demo-kit encodes H.264: on the GPU through
// VAAPI when that works, else with x264 on the CPU. desk rec and demo
// build ask it the same way, so both pick the same encoder on a machine.
//
//	enc, err := encode.Pick(cfg.Video)
//	args := append(enc.Device(), "-i", in, "-vf", "scale=…"+enc.Upload())
//	args = append(args, enc.Codec(24)...)
package encode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/keyclicker/demo-kit/internal/config"
)

// Encoder is one way to encode H.264. The zero value is x264.
type Encoder struct {
	// Node is the VAAPI render node; "" means x264 on the CPU.
	Node string
}

// VAAPI reports whether e encodes on the GPU.
func (e Encoder) VAAPI() bool { return e.Node != "" }

// Name is "vaapi" or "x264", as the config spells it.
func (e Encoder) Name() string {
	if e.VAAPI() {
		return "vaapi"
	}
	return "x264"
}

// Device returns the ffmpeg arguments that go before the inputs: the
// VAAPI device, or nothing.
func (e Encoder) Device() []string {
	if e.VAAPI() {
		return []string{"-vaapi_device", e.Node}
	}
	return nil
}

// Upload returns the tail of a filter chain that hands frames to the
// encoder: into GPU memory as nv12, or as yuv420p for x264. It starts
// with a comma, to follow other filters.
func (e Encoder) Upload() string {
	if e.VAAPI() {
		return ",format=nv12,hwupload"
	}
	return ",format=yuv420p"
}

// Codec returns the codec arguments at quality q: VAAPI's constant QP,
// or x264's CRF, which land close on the same number. Lower is better;
// 18 is near lossless, 28 is fine for screen recordings.
func (e Encoder) Codec(q int) []string {
	if e.VAAPI() {
		return []string{"-c:v", "h264_vaapi", "-qp", fmt.Sprint(q)}
	}
	return []string{"-c:v", "libx264", "-preset", "veryfast",
		"-crf", fmt.Sprint(q)}
}

// ===== Picking =====

// Pick resolves the [video] settings to an encoder. "auto" runs a tiny
// test encode on the render node (well under a second), so a GPU without
// a working VAAPI driver falls back to x264 instead of failing later.
func Pick(v config.Video) (Encoder, error) {
	if v.Encoder == "x264" {
		return Encoder{}, nil
	}

	node, err := RenderNode(v.RenderNode)
	if err != nil {
		return Encoder{}, err
	}

	switch {
	case v.Encoder == "vaapi" && node == "":
		return Encoder{}, errors.New("video.encoder is vaapi, but there " +
			"is no render node (video.render_node)")

	case v.Encoder == "vaapi":
		return Encoder{Node: node}, nil

	case node != "" && probe(node):
		return Encoder{Node: node}, nil
	}

	return Encoder{}, nil
}

// RenderNode resolves a render_node setting: "auto" is the first
// /dev/dri/renderD* ("" when there is none), "" is no GPU, and a path
// must exist.
func RenderNode(setting string) (string, error) {
	switch setting {
	case "":
		return "", nil

	case "auto":
		nodes, _ := filepath.Glob("/dev/dri/renderD*")
		sort.Strings(nodes)
		if len(nodes) == 0 {
			return "", nil
		}
		return nodes[0], nil
	}

	if _, err := os.Stat(setting); err != nil {
		return "", fmt.Errorf("video.render_node: %w", err)
	}
	return setting, nil
}

// probe reports whether VAAPI encodes on node: a few frames of a test
// pattern, encoded and thrown away. 256x256, as GPUs have a minimum size
// (AMD's is 128x128).
func probe(node string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	enc := Encoder{Node: node}
	args := append([]string{"-loglevel", "error", "-nostdin"},
		enc.Device()...)
	args = append(args, "-f", "lavfi", "-i", "testsrc2=size=256x256:rate=5",
		"-frames:v", "5", "-vf", enc.Upload()[1:])
	args = append(args, enc.Codec(28)...)
	args = append(args, "-f", "null", "-")

	return exec.CommandContext(ctx, "ffmpeg", args...).Run() == nil
}
