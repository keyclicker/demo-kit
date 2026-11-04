package main

import (
	"errors"
	"strings"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// maximizeClasses are the apps agents work inside: their main windows
// open maximized, once. Matched against WM_CLASS's class, lowercased;
// an entry also covers "entry-*" (libreoffice-writer, ...).
var maximizeClasses = []string{
	"org.chromium.chromium", // Flatpak Chromium, agent-chromium
	"chromium-browser",      // agent-browser's Chrome for Testing
	"firefox",
	"libreoffice",
	"papers",
	"gnome-text-editor",
	"gimp",
	"inkscape",
	"blender",
	"freecad",
	"godot",
	"t3code",
	"com.mitchellh.ghostty", // terminals for TUI and CLI demos
	"xterm",
}

// shouldMaximize reports whether a WM_CLASS class is on the list.
func shouldMaximize(class string) bool {
	class = strings.ToLower(class)

	for _, c := range maximizeClasses {
		if class == c || strings.HasPrefix(class, c+"-") {
			return true
		}
	}

	return false
}

// atoms are the EWMH names maximizeNew needs.
type atoms struct {
	clientList, windowType, typeNormal  xproto.Atom
	state, maximizedVert, maximizedHorz xproto.Atom
}

// maximizeNew maximizes listed apps' windows on display as they open.
// Each window is looked at once, when it first shows up in xfwm4's
// _NET_CLIENT_LIST, so agents can resize it afterwards. Dialogs,
// transients and unlisted apps keep their own size. Runs until the
// X server goes away.
func maximizeNew(display string) error {
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		return err
	}
	defer conn.Close()

	root := xproto.Setup(conn).DefaultScreen(conn).Root

	a, err := internAtoms(conn)
	if err != nil {
		return err
	}

	// xfwm4 updates _NET_CLIENT_LIST on root as windows come and go
	err = xproto.ChangeWindowAttributesChecked(conn, root, xproto.CwEventMask,
		[]uint32{xproto.EventMaskPropertyChange}).Check()
	if err != nil {
		return err
	}

	seen := map[xproto.Window]bool{}
	for {
		seen = scanClients(conn, root, a, seen)

		// Block until the client list changes
		for {
			ev, err := conn.WaitForEvent()
			if ev == nil && err == nil {
				return errors.New("X connection closed")
			}

			p, ok := ev.(xproto.PropertyNotifyEvent)
			if ok && p.Atom == a.clientList {
				break
			}
		}
	}
}

// scanClients maximizes the windows not in seen yet, and returns the
// new seen set: the current list, so closed windows drop out.
func scanClients(conn *xgb.Conn, root xproto.Window, a atoms,
	seen map[xproto.Window]bool) map[xproto.Window]bool {

	now := map[xproto.Window]bool{}

	for _, w := range windowList(conn, root, a.clientList) {
		now[w] = true

		if !seen[w] && isAppWindow(conn, w, a) {
			maximize(conn, root, w, a)
		}
	}

	return now
}

// isAppWindow: a listed app's normal, non-transient window.
func isAppWindow(conn *xgb.Conn, w xproto.Window, a atoms) bool {
	if len(windowList(conn, w, xproto.AtomWmTransientFor)) > 0 {
		return false
	}

	// No type at all means normal (EWMH), e.g. Blender
	if types := atomList(conn, w, a.windowType); len(types) > 0 &&
		types[0] != a.typeNormal {
		return false
	}

	// WM_CLASS is "instance\0class\0"
	raw := property(conn, w, xproto.AtomWmClass)
	parts := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")

	return len(parts) == 2 && shouldMaximize(parts[1])
}

// maximize asks the window manager to maximize w (EWMH _NET_WM_STATE).
func maximize(conn *xgb.Conn, root, w xproto.Window, a atoms) {
	const stateAdd, sourceApp = 1, 1

	data := xproto.ClientMessageDataUnionData32New([]uint32{
		stateAdd, uint32(a.maximizedVert), uint32(a.maximizedHorz),
		sourceApp, 0,
	})
	ev := xproto.ClientMessageEvent{
		Format: 32, Window: w, Type: a.state, Data: data,
	}

	xproto.SendEvent(conn, false, root,
		xproto.EventMaskSubstructureNotify|xproto.EventMaskSubstructureRedirect,
		string(ev.Bytes()))
}

// ===== X helpers =====

func internAtoms(conn *xgb.Conn) (atoms, error) {
	var a atoms
	targets := map[string]*xproto.Atom{
		"_NET_CLIENT_LIST":             &a.clientList,
		"_NET_WM_WINDOW_TYPE":          &a.windowType,
		"_NET_WM_WINDOW_TYPE_NORMAL":   &a.typeNormal,
		"_NET_WM_STATE":                &a.state,
		"_NET_WM_STATE_MAXIMIZED_VERT": &a.maximizedVert,
		"_NET_WM_STATE_MAXIMIZED_HORZ": &a.maximizedHorz,
	}

	for name, dst := range targets {
		r, err := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply()
		if err != nil {
			return a, err
		}
		*dst = r.Atom
	}

	return a, nil
}

// property reads a whole property's raw value; nil if unset or gone.
func property(conn *xgb.Conn, w xproto.Window, prop xproto.Atom) []byte {
	r, err := xproto.GetProperty(conn, false, w, prop,
		xproto.GetPropertyTypeAny, 0, 1<<16).Reply()
	if err != nil {
		return nil
	}

	return r.Value
}

// ids reads a property of 32-bit IDs (windows, atoms).
func ids(conn *xgb.Conn, w xproto.Window, prop xproto.Atom) []uint32 {
	raw := property(conn, w, prop)

	out := make([]uint32, 0, len(raw)/4)
	for i := 0; i+4 <= len(raw); i += 4 {
		out = append(out, xgb.Get32(raw[i:]))
	}

	return out
}

func windowList(conn *xgb.Conn, w xproto.Window, prop xproto.Atom) []xproto.Window {
	var out []xproto.Window
	for _, id := range ids(conn, w, prop) {
		out = append(out, xproto.Window(id))
	}

	return out
}

func atomList(conn *xgb.Conn, w xproto.Window, prop xproto.Atom) []xproto.Atom {
	var out []xproto.Atom
	for _, id := range ids(conn, w, prop) {
		out = append(out, xproto.Atom(id))
	}

	return out
}
