// Sound for the page: VNC carries none, so a PipeWire sink's monitor is
// captured with pw-record and streamed as raw PCM to every listener.
// One stream for the whole login session: every display plays into it.
package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strconv"
)

// audioCheck says why the page has no sound, or nil when it has.
func audioCheck() error {
	if !cfg.Deskview.Audio {
		return errors.New("off in the config (deskview.audio)")
	}
	if _, err := exec.LookPath("pw-record"); err != nil {
		return errors.New("no pw-record (PipeWire)")
	}
	return nil
}

// pwRecord captures a sink's monitor as s16le stereo at 48 kHz; sink ""
// is the default output.
func pwRecord(sink string) Source {
	return func(ctx context.Context) (io.ReadCloser, error) {
		var args []string
		if sink != "" {
			args = append(args, "--target", sink)
		}
		args = append(args,
			"-P", "{ stream.capture.sink = true node.name = deskview }",
			"--raw", "--format", "s16", "--rate", "48000",
			"--channels", "2", "--latency", "20ms", "-")

		cmd := exec.CommandContext(ctx, "pw-record", args...)

		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}

		return &cmdReader{ReadCloser: out, cmd: cmd}, nil
	}
}

// cmdReader reaps its process on Close.
type cmdReader struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (r *cmdReader) Close() error {
	r.ReadCloser.Close()
	return r.cmd.Wait()
}

// streamHandler sends the live PCM until the client goes away. The
// format travels in headers, so the page doesn't hardcode it.
func streamHandler(b *Broadcaster) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flush := http.NewResponseController(w)

		h := w.Header()
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Accel-Buffering", "no")
		h.Set("X-Audio-Rate", strconv.Itoa(48000))
		h.Set("X-Audio-Channels", "2")
		h.Set("X-Audio-Format", "s16le")
		w.WriteHeader(http.StatusOK)
		flush.Flush()

		ch := b.Subscribe()
		defer b.Unsubscribe(ch)

		for {
			select {
			case <-r.Context().Done():
				return
			case buf, ok := <-ch:
				if !ok {
					return // capture ended; the page reconnects
				}
				if _, err := w.Write(buf); err != nil {
					return
				}
				if err := flush.Flush(); err != nil {
					return
				}
			}
		}
	})
}
