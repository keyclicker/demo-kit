package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"
)

// vncProxy bridges noVNC's WebSocket to a display's VNC TCP port: what
// websockify does, for the displays desk lists and nothing else.
type vncProxy struct{ desk string }

func (p vncProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	addr, ok := p.vncAddr(r.Context(), r.PathValue("token"))
	if !ok {
		http.Error(w, "no such display", http.StatusNotFound)
		return
	}

	if !sameOrigin(r) {
		http.Error(w, "cross-origin", http.StatusForbidden)
		return
	}

	// noVNC asks for the "binary" subprotocol. Origin is checked above,
	// proxies included
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"binary"},
		InsecureSkipVerify: true,
	})
	if err != nil {
		return // Accept has answered
	}
	defer ws.CloseNow()

	ws.SetReadLimit(-1) // VNC messages (clipboard, big rects) can be large

	tcp, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		log.Printf("vnc %s: %v", addr, err)
		ws.Close(websocket.StatusTryAgainLater, "display unreachable")
		return
	}
	defer tcp.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)

	// Either side closing ends both
	done := make(chan struct{}, 2)
	go func() { io.Copy(tcp, conn); done <- struct{}{} }()
	go func() { io.Copy(conn, tcp); done <- struct{}{} }()
	<-done
}

// vncAddr looks token up in desk's list: only listed displays with a
// VNC address are reachable, so the proxy can't be pointed elsewhere.
func (p vncProxy) vncAddr(ctx context.Context, token string) (string, bool) {
	out, err := listDesktops(ctx, p.desk)
	if err != nil {
		log.Printf("desk ls: %v", err)
		return "", false
	}

	var desks []Desktop
	if json.Unmarshal(out, &desks) != nil {
		return "", false
	}

	for _, d := range desks {
		if d.Token == token && d.VNC != "" {
			return d.VNC, true
		}
	}
	return "", false
}

// sameOrigin accepts a WebSocket from the page itself: the Origin host is
// the request's host, or the host a proxy forwarded (X-Forwarded-Host),
// as reverse proxies may rewrite Host, when that host is known too
// (knownHost: a request header alone never makes one). No Origin is
// not a browser, so no CSRF risk. An Origin with no host (`null` from
// sandboxed iframes and data: pages, file://) is any page: refused.
// Hosts compare exactly, port included.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}

	if u.Host == r.Host {
		return true
	}

	// An absent header is "": never let it match
	fwd := r.Header.Get("X-Forwarded-Host")
	return fwd != "" && u.Host == fwd && knownHost(fwd)
}
