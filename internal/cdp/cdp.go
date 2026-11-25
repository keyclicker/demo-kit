// Package cdp speaks the Chrome DevTools Protocol over one WebSocket:
// calls answered by id, events delivered by name, and tabs as flat
// sessions (Target.attachToTarget with flatten) on the same socket.
//
// Just what demo-kit needs: no generated types, params and results
// are any JSON-able value.
package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/coder/websocket"
)

// readLimit caps one message: a tall page's screenshot is tens of MB
// of base64.
const readLimit = 256 << 20

// Conn is a DevTools connection, browser-wide. Safe for concurrent
// use.
type Conn struct {
	ws   *websocket.Conn
	stop context.CancelFunc

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan message
	subs    map[event][]chan json.RawMessage

	done chan struct{} // closed when reading stops; err says why
	err  error
}

// event names a subscription: a method on a session ("" is the
// browser's).
type event struct{ session, method string }

// message is any DevTools message: a call, its answer, or an event.
type message struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}

// Error is the browser's answer to a call that failed.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Data)
	}
	return e.Message
}

// Dial connects to a DevTools WebSocket URL, e.g. the browser's
// webSocketDebuggerUrl.
func Dial(ctx context.Context, url string) (*Conn, error) {
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("devtools: %w", err)
	}
	ws.SetReadLimit(readLimit)

	readCtx, stop := context.WithCancel(context.Background())
	c := &Conn{ws: ws, stop: stop, pending: map[int64]chan message{},
		subs: map[event][]chan json.RawMessage{}, done: make(chan struct{})}
	go c.read(readCtx)
	return c, nil
}

// Close drops the connection; calls in flight fail.
func (c *Conn) Close() error {
	c.stop()
	return c.ws.CloseNow()
}

// Call runs method on session ("" for the browser) with params (nil
// for none) and decodes its result into result (nil to drop it).
func (c *Conn) Call(ctx context.Context, session, method string,
	params, result any) error {

	var raw json.RawMessage
	if params != nil {
		var err error
		if raw, err = json.Marshal(params); err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
	}

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	answer := make(chan message, 1)
	c.pending[id] = answer
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	data, err := json.Marshal(message{ID: id, Method: method, Params: raw,
		SessionID: session})
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}

	select {
	case m := <-answer:
		if m.Error != nil {
			return fmt.Errorf("%s: %w", method, m.Error)
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(m.Result, result); err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		return nil

	case <-c.done:
		return fmt.Errorf("%s: %w", method, c.err)
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

// Events delivers session's method events (their params) until cancel
// is called. Subscribe before the call that causes them. The channel
// holds buffer events; more are dropped until it's read.
func (c *Conn) Events(session, method string, buffer int) (
	events <-chan json.RawMessage, cancel func()) {

	ch := make(chan json.RawMessage, buffer)
	key := event{session, method}

	c.mu.Lock()
	c.subs[key] = append(c.subs[key], ch)
	c.mu.Unlock()

	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, sub := range c.subs[key] {
			if sub == ch {
				c.subs[key] = append(c.subs[key][:i], c.subs[key][i+1:]...)
				break
			}
		}
	}
}

// Done is closed once the connection is gone: the browser quit, or
// Close.
func (c *Conn) Done() <-chan struct{} { return c.done }

// read hands each message to its call or its subscribers, until the
// socket ends.
func (c *Conn) read(ctx context.Context) {
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			c.err = fmt.Errorf("devtools: connection gone: %w", err)
			close(c.done)
			return
		}

		var m message
		if json.Unmarshal(data, &m) != nil {
			continue // not ours to fail on: a call times out instead
		}

		c.mu.Lock()
		if m.ID != 0 {
			if answer, ok := c.pending[m.ID]; ok {
				answer <- m // buffered, and answered once
			}
		} else {
			for _, sub := range c.subs[event{m.SessionID, m.Method}] {
				select {
				case sub <- m.Params:
				default:
				}
			}
		}
		c.mu.Unlock()
	}
}
