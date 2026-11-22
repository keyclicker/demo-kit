// Command deskview is the viewer for desk displays: one web page with a
// noVNC tab per display, its sound, and the published files.
//
//	/                 the page (web/index.html)
//	/novnc/           noVNC's client, embedded
//	/api/desktops     what `desk ls --json` prints
//	/api/audio        whether the page has sound, and why not
//	/vnc/{token}      WebSocket ↔ that display's VNC
//	/audio/stream     raw PCM from a PipeWire sink
//	/files/           publish.root, read-only, dot-names hidden
//
// It listens on deskview.addr, loopback by default: put a tunnel or a
// proxy with auth in front of it (tailscale serve, a reverse proxy). Every
// URL the page uses is relative, so it works under any path prefix.
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"

	"github.com/keyclicker/demo-kit/internal/config"
)

const usageText = `deskview: watch desk displays in a browser

  deskview               serve the page on deskview.addr
  deskview config [KEY]  print the config in effect, or one key's value

Displays come from desk ls; desk's own are view-only, the host display
(desk.host_vnc) interactive. Sound needs PipeWire.
`

// web is the page and noVNC, built into the binary.
//
//go:embed web
var web embed.FS

// cfg is the config in effect, set once by main.
var cfg config.Config

func main() {
	log.SetFlags(0)
	log.SetPrefix("deskview: ")

	var err error
	if cfg, err = config.Load(); err != nil {
		log.Fatal(err)
	}

	args := os.Args[1:]
	switch {
	case len(args) == 0:
		err = serve()
	case args[0] == "config":
		err = config.Cmd("deskview", cfg, args[1:], os.Stdout)
	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	if err != nil {
		log.Fatal(err)
	}
}

// serve runs the server until it fails.
func serve() error {
	desk, err := deskBinary()
	if err != nil {
		return err
	}

	page, err := fs.Sub(web, "web")
	if err != nil {
		return err
	}

	audio := NewBroadcaster(pwRecord(cfg.Deskview.AudioSink))

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(page))
	mux.Handle("GET /api/desktops", desktopsAPI{desk})
	mux.HandleFunc("GET /api/audio", audioAPI)
	mux.Handle("GET /vnc/{token}", vncProxy{desk})
	mux.Handle("GET /audio/stream", streamHandler(audio))
	mux.Handle("GET /files/", http.StripPrefix("/files",
		http.FileServerFS(hideDots{os.DirFS(cfg.Publish.Root)})))

	if err := audioCheck(); err != nil {
		log.Printf("no sound: %v", err)
	}
	log.Printf("http://%s/ (displays from %s)", cfg.Deskview.Addr, desk)

	return http.ListenAndServe(cfg.Deskview.Addr, mux)
}
