package main

import (
	"context"
	"io"
	"log"
	"sync"
	"time"
)

// chunkBytes is 20 ms of s16le stereo at 48 kHz.
const chunkBytes = 48000 * 2 * 2 / 50

// clientQueue is how many chunks a slow listener may lag before
// chunks get dropped for it (8 × 20 ms).
const clientQueue = 8

// Source opens a raw PCM capture that runs until ctx is cancelled.
type Source func(ctx context.Context) (io.ReadCloser, error)

// Broadcaster fans one capture out to every listener. The capture runs
// only while at least one listener is subscribed.
type Broadcaster struct {
	open Source

	mu      sync.Mutex
	clients map[chan []byte]struct{}
	stop    context.CancelFunc
}

// NewBroadcaster returns a Broadcaster that captures from open.
func NewBroadcaster(open Source) *Broadcaster {
	return &Broadcaster{open: open, clients: map[chan []byte]struct{}{}}
}

// Subscribe registers a listener and starts the capture if it's the
// first one. The channel closes when the capture ends.
func (b *Broadcaster) Subscribe() chan []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan []byte, clientQueue)
	b.clients[ch] = struct{}{}

	if b.stop == nil {
		ctx, cancel := context.WithCancel(context.Background())
		b.stop = cancel
		go b.run(ctx)
	}

	return ch
}

// Unsubscribe drops a listener and stops the capture after the last.
func (b *Broadcaster) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.clients[ch]; !ok {
		return // already closed by run
	}
	delete(b.clients, ch)
	close(ch)

	if len(b.clients) == 0 && b.stop != nil {
		b.stop()
		b.stop = nil
	}
}

// run reads chunks from one capture and hands them to every listener.
func (b *Broadcaster) run(ctx context.Context) {
	defer b.closeAll(ctx)

	src, err := b.open(ctx)
	if err != nil {
		log.Printf("capture: %v", err)
		time.Sleep(time.Second) // don't let reconnecting clients spin
		return
	}
	defer src.Close()

	for {
		buf := make([]byte, chunkBytes)
		if _, err := io.ReadFull(src, buf); err != nil {
			if ctx.Err() == nil {
				log.Printf("capture ended: %v", err)
			}
			return
		}
		b.send(buf)
	}
}

// send queues a chunk for every listener, dropping it for full queues.
func (b *Broadcaster) send(buf []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for ch := range b.clients {
		select {
		case ch <- buf:
		default: // listener lags: drop rather than grow latency
		}
	}
}

// closeAll ends every listener of the capture that ran under ctx.
func (b *Broadcaster) closeAll(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if ctx.Err() != nil {
		return // stopped by Unsubscribe: no listeners left
	}
	for ch := range b.clients {
		delete(b.clients, ch)
		close(ch)
	}
	b.stop()
	b.stop = nil
}
