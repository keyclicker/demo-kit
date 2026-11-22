package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Desktop is the part of a `desk ls --json` entry deskview uses.
type Desktop struct {
	Display  string `json:"display"`
	Token    string `json:"token"`
	ViewOnly bool   `json:"viewOnly"`

	// VNC is the display's VNC address; empty means not viewable.
	VNC string `json:"vnc"`
}

// deskBinary finds desk: beside deskview first (a user unit's PATH may
// lack ~/.local/bin), then on PATH.
func deskBinary() (string, error) {
	if self, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(self), "desk")
		if _, err := os.Stat(sibling); err == nil {
			return sibling, nil
		}
	}

	path, err := exec.LookPath("desk")
	if err != nil {
		return "", errors.New("no desk binary beside deskview or on PATH")
	}
	return path, nil
}

// listDesktops runs `desk ls --json`, the contract between the two.
func listDesktops(ctx context.Context, desk string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, desk, "ls", "--json").Output()
	if err != nil {
		return nil, err
	}
	if !json.Valid(out) {
		return nil, errors.New("desk ls --json: not JSON")
	}
	return out, nil
}

// desktopsAPI serves `desk ls --json`, polled by the page to keep one
// tab per display.
type desktopsAPI struct{ desk string }

func (a desktopsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	out, err := listDesktops(r.Context(), a.desk)
	if err != nil {
		log.Printf("desk ls: %v", err)
		http.Error(w, "desk ls failed", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(out)
}

// audioAPI tells the page whether 🔊 works, and if not, why.
func audioAPI(w http.ResponseWriter, _ *http.Request) {
	status := struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason,omitempty"`
	}{Available: true}

	if err := audioCheck(); err != nil {
		status.Available, status.Reason = false, err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(status)
}
