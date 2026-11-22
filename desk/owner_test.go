package main

import (
	"os"
	"testing"
)

func TestParseStat(t *testing.T) {
	// comm may hold spaces and parens; starttime is field 22
	stat := "4242 (tmux: server (x)) S 17 4242 4242 0 -1 4194560 " +
		"1 2 3 4 5 6 7 8 20 0 1 0 987654 1 2 3\n"

	o, err := parseStat(4242, stat)
	if err != nil {
		t.Fatal(err)
	}

	want := Owner{PID: 4242, Name: "tmux: server (x)", Start: 987654, ppid: 17}
	if o != want {
		t.Fatalf("got %+v, want %+v", o, want)
	}

	if _, err := parseStat(1, "1 (short) S 0"); err == nil {
		t.Fatal("accepted a short stat")
	}
}

func TestOwnerLineRoundTrip(t *testing.T) {
	in := Owner{PID: 2258093, Start: 123456, Name: "node-MainThread x"}

	out, ok := parseOwnerLine(ownerLine(in))
	if !ok || !out.Same(in) || out.Name != in.Name {
		t.Fatalf("round trip: %+v -> %+v (%v)", in, out, ok)
	}

	if _, ok := parseOwnerLine("10: 127.0.0.1:5910"); ok {
		t.Fatal("parsed a token line as an owner")
	}
}

func TestCallerOwnerSkipsShells(t *testing.T) {
	o, err := callerOwner()
	if err != nil {
		t.Fatal(err)
	}
	if wrappers[o.Name] || !o.Alive() {
		t.Fatalf("owner %+v: a wrapper, or not running", o)
	}

	t.Setenv("DESK_OWNER_PID", "1")
	if o, err := callerOwner(); err != nil || o.PID != 1 {
		t.Fatalf("DESK_OWNER_PID ignored: %+v, %v", o, err)
	}

	// A dead PID's start time never matches
	ghost := Owner{PID: os.Getpid(), Start: 1}
	if ghost.Alive() {
		t.Fatal("PID reuse not detected")
	}
}
