package encode

import (
	"os/exec"
	"slices"
	"testing"

	"github.com/keyclicker/demo-kit/internal/config"
)

func TestX264(t *testing.T) {
	enc, err := Pick(config.Video{RenderNode: "auto", Encoder: "x264"})
	if err != nil || enc.VAAPI() {
		t.Fatalf("Pick(x264) = %+v, %v", enc, err)
	}

	if enc.Device() != nil || enc.Upload() != ",format=yuv420p" ||
		!slices.Contains(enc.Codec(24), "libx264") {
		t.Errorf("x264 args: %v %q %v", enc.Device(), enc.Upload(),
			enc.Codec(24))
	}
}

func TestNoGPU(t *testing.T) {
	enc, err := Pick(config.Video{RenderNode: "", Encoder: "auto"})
	if err != nil || enc.VAAPI() {
		t.Errorf("auto without a GPU = %+v, %v; want x264", enc, err)
	}

	if _, err := Pick(config.Video{RenderNode: "", Encoder: "vaapi"}); err == nil {
		t.Error("vaapi without a GPU: no error")
	}

	if _, err := RenderNode("/dev/dri/nope"); err == nil {
		t.Error("missing render node: no error")
	}
}

// TestAutoEncodes checks that whatever auto picks really encodes here.
func TestAutoEncodes(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}

	enc, err := Pick(config.Video{RenderNode: "auto", Encoder: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("auto picked %s %s", enc.Name(), enc.Node)

	args := append([]string{"-loglevel", "error", "-nostdin"}, enc.Device()...)
	args = append(args, "-f", "lavfi", "-i", "testsrc2=size=256x256:rate=5",
		"-frames:v", "5", "-vf", "null"+enc.Upload())
	args = append(args, enc.Codec(28)...)
	args = append(args, "-f", "null", "-")

	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Errorf("%s encode: %v\n%s", enc.Name(), err, out)
	}
}
