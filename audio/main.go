// deskaudio streams a PipeWire sink to browsers as raw PCM, so the
// /desktops page can play :0's sound next to its noVNC view.
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strconv"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8091", "listen address")
	sink := flag.String("sink", "desktop-speaker", "PipeWire sink to capture")
	flag.Parse()

	b := NewBroadcaster(pwRecord(*sink))

	mux := http.NewServeMux()
	mux.Handle("GET /stream", streamHandler(b))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok\n")
	})

	log.Printf("deskaudio: %s on %s", *sink, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// pwRecord captures a sink's monitor as s16le stereo at 48 kHz.
func pwRecord(sink string) Source {
	return func(ctx context.Context) (io.ReadCloser, error) {
		cmd := exec.CommandContext(ctx, "pw-record",
			"--target", sink,
			"-P", "{ stream.capture.sink = true node.name = deskaudio }",
			"--raw", "--format", "s16", "--rate", "48000",
			"--channels", "2", "--latency", "20ms", "-")

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
