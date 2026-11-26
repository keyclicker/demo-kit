package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keyclicker/demo-kit/internal/config"
)

// fakeSource yields endless zero chunks and counts open captures.
func fakeSource(open *atomic.Int32) Source {
	return func(ctx context.Context) (io.ReadCloser, error) {
		open.Add(1)
		r, w := io.Pipe()
		go func() {
			chunk := make([]byte, chunkBytes)
			for ctx.Err() == nil {
				if _, err := w.Write(chunk); err != nil {
					return
				}
				time.Sleep(time.Millisecond)
			}
			w.CloseWithError(ctx.Err())
			open.Add(-1)
		}()
		return r, nil
	}
}

func recv(t *testing.T, ch chan []byte) []byte {
	t.Helper()
	select {
	case buf := <-ch:
		return buf
	case <-time.After(time.Second):
		t.Fatal("no chunk within 1s")
		return nil
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for range 100 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

func TestFanOutAndStopAfterLastListener(t *testing.T) {
	var open atomic.Int32
	b := NewBroadcaster(fakeSource(&open))

	a, c := b.Subscribe(), b.Subscribe()
	if got := recv(t, a); len(got) != chunkBytes {
		t.Fatalf("chunk = %d bytes, want %d", len(got), chunkBytes)
	}
	recv(t, c)
	if n := open.Load(); n != 1 {
		t.Fatalf("captures = %d, want 1 shared", n)
	}

	b.Unsubscribe(a)
	recv(t, c) // still flowing for the other listener

	b.Unsubscribe(c)
	eventually(t, func() bool { return open.Load() == 0 })
}

func TestCaptureEndClosesListeners(t *testing.T) {
	b := NewBroadcaster(func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(make([]byte, chunkBytes))), nil
	})

	ch := b.Subscribe()
	recv(t, ch)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("got a chunk after the capture ended")
		}
	case <-time.After(time.Second):
		t.Fatal("listener not closed after capture ended")
	}

	b.Unsubscribe(ch) // must not double-close
}

// deskview.audio = false closes the stream too, not just api/audio:
// nothing is captured
func TestStreamObeysAudioOff(t *testing.T) {
	defer func(c config.Config) { cfg = c }(cfg)
	cfg.Deskview.Audio = false

	var open atomic.Int32
	rec := httptest.NewRecorder()
	streamHandler(NewBroadcaster(fakeSource(&open))).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/audio/stream", nil))

	if rec.Code != http.StatusServiceUnavailable ||
		!strings.Contains(rec.Body.String(), "deskview.audio") {
		t.Errorf("stream: %d %q; want 503 naming deskview.audio", rec.Code,
			rec.Body.String())
	}
	if open.Load() != 0 {
		t.Error("captured with sound off")
	}
}
