package cdp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeBrowser answers Echo with its params, Fail with an error, and
// before answering Load sends Page.loaded on the caller's session.
func fakeBrowser(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {

		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()

		for {
			_, data, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var m message
			json.Unmarshal(data, &m)

			var out []message
			switch m.Method {
			case "Echo":
				out = []message{{ID: m.ID, Result: m.Params}}
			case "Fail":
				out = []message{{ID: m.ID, Error: &Error{Code: -1,
					Message: "no such thing"}}}
			case "Load":
				out = []message{
					{Method: "Page.loaded", SessionID: m.SessionID,
						Params: json.RawMessage(`{"n":1}`)},
					{ID: m.ID, Result: json.RawMessage(`{}`)}}
			}
			for _, o := range out {
				data, _ := json.Marshal(o)
				ws.Write(r.Context(), websocket.MessageText, data)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestConn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Dial(ctx, fakeBrowser(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var got struct{ X int }
	if err := c.Call(ctx, "", "Echo", map[string]int{"X": 7}, &got); err != nil ||
		got.X != 7 {
		t.Fatalf("echo: %+v, %v", got, err)
	}

	err = c.Call(ctx, "", "Fail", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "Fail: no such thing") {
		t.Fatalf("fail: %v", err)
	}

	// Events reach their session's subscribers only
	mine, stop := c.Events("s1", "Page.loaded", 1)
	defer stop()
	other, stopOther := c.Events("s2", "Page.loaded", 1)
	defer stopOther()

	if err := c.Call(ctx, "s1", "Load", nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-mine:
		if string(p) != `{"n":1}` {
			t.Fatalf("event params %s", p)
		}
	default:
		t.Fatal("no event before the answer")
	}
	select {
	case <-other:
		t.Fatal("another session's event")
	default:
	}

	// Calls after the connection ends fail, not hang
	c.Close()
	if err := c.Call(ctx, "", "Echo", nil, nil); err == nil {
		t.Fatal("call on a closed connection")
	}
}
