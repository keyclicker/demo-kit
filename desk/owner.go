package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Owner is the process a desktop belongs to: the agent that ran
// `desk new`. Start (clock ticks since boot) tells a reused PID apart.
type Owner struct {
	PID   int    `json:"pid"`
	Name  string `json:"name"`
	Start uint64 `json:"-"`

	ppid int
}

// wrappers sit between an agent and desk; the owner is the first
// ancestor that isn't one of them.
var wrappers = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true,
	"env": true, "nohup": true, "setsid": true, "timeout": true,
	"sudo": true, "xargs": true, "time": true, "stdbuf": true,
}

// callerOwner finds the process that owns desktops desk creates now:
// $DESK_OWNER_PID if set, else the nearest ancestor that isn't a shell.
// For agents that's the agent itself (claude, codex), since each tool
// call is a short-lived shell.
func callerOwner() (Owner, error) {
	if s := os.Getenv("DESK_OWNER_PID"); s != "" {
		pid, err := strconv.Atoi(s)
		if err != nil {
			return Owner{}, fmt.Errorf("DESK_OWNER_PID=%q: %w", s, err)
		}
		return procOwner(pid)
	}

	for pid := os.Getppid(); pid > 1; {
		o, err := procOwner(pid)
		if err != nil {
			return Owner{}, err
		}
		if !wrappers[o.Name] {
			return o, nil
		}
		pid = o.ppid
	}

	return Owner{}, errors.New("no owner process: only shells above desk")
}

// procOwner reads a process's identity from /proc/PID/stat.
func procOwner(pid int) (Owner, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return Owner{}, err
	}

	return parseStat(pid, string(data))
}

// parseStat parses /proc/PID/stat: "pid (comm) state ppid ...", where
// comm may hold spaces and parens, and starttime is field 22.
func parseStat(pid int, stat string) (Owner, error) {
	open := strings.IndexByte(stat, '(')
	shut := strings.LastIndexByte(stat, ')')
	if open < 0 || shut < open {
		return Owner{}, fmt.Errorf("pid %d: malformed stat", pid)
	}

	// Fields after comm start at field 3 (state)
	rest := strings.Fields(stat[shut+1:])
	if len(rest) < 20 {
		return Owner{}, fmt.Errorf("pid %d: short stat", pid)
	}

	ppid, err1 := strconv.Atoi(rest[1])
	start, err2 := strconv.ParseUint(rest[19], 10, 64)
	if err := errors.Join(err1, err2); err != nil {
		return Owner{}, fmt.Errorf("pid %d: stat: %w", pid, err)
	}

	return Owner{
		PID:   pid,
		Name:  stat[open+1 : shut],
		Start: start,
		ppid:  ppid,
	}, nil
}

// Alive reports whether the owner process still runs: same PID, same
// start time.
func (o Owner) Alive() bool {
	now, err := procOwner(o.PID)
	return err == nil && now.Start == o.Start
}

// Same reports whether two owners are the same process.
func (o Owner) Same(other Owner) bool {
	return o.PID == other.PID && o.Start == other.Start
}

func (o Owner) String() string {
	return fmt.Sprintf("%s %d", o.Name, o.PID)
}

// ---- Token file encoding ----

const ownerPrefix = "# owner: "

// ownerLine encodes an owner as a token file comment websockify skips.
func ownerLine(o Owner) string {
	return fmt.Sprintf("%s%d %d %s", ownerPrefix, o.PID, o.Start, o.Name)
}

// parseOwnerLine decodes ownerLine's output; ok is false otherwise.
func parseOwnerLine(line string) (o Owner, ok bool) {
	rest, found := strings.CutPrefix(line, ownerPrefix)
	if !found {
		return Owner{}, false
	}

	parts := strings.SplitN(rest, " ", 3)
	if len(parts) != 3 {
		return Owner{}, false
	}

	pid, err1 := strconv.Atoi(parts[0])
	start, err2 := strconv.ParseUint(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return Owner{}, false
	}

	return Owner{PID: pid, Start: start, Name: parts[2]}, true
}

// parseOwnerArg decodes `desk run`'s -owner value, "PID:START".
func parseOwnerArg(s string) (Owner, error) {
	pidStr, startStr, found := strings.Cut(s, ":")
	pid, err1 := strconv.Atoi(pidStr)
	start, err2 := strconv.ParseUint(startStr, 10, 64)
	if !found || err1 != nil || err2 != nil {
		return Owner{}, fmt.Errorf("-owner %q: want PID:START", s)
	}

	o, err := procOwner(pid)
	if err != nil || o.Start != start {
		return Owner{}, fmt.Errorf("owner %d exited before its desktop "+
			"started", pid)
	}

	return o, nil
}
