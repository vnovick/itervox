package sysmon

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/logging"
)

// Per-run activity caps. Records are deduplicated (a command line, a path or
// a remote address is one record with a count), so a cap bounds DISTINCT
// entries; beyond it new ones are counted in Dropped.
const (
	maxActivityCommands    = 500
	maxActivityFiles       = 1000
	maxActivityConnections = 300
	pathMaxBytes           = 400
)

// CommandRecord is a command a run started.
type CommandRecord struct {
	Name    string    `json:"name"`
	Args    string    `json:"args,omitempty"`
	PID     int       `json:"pid"`
	Count   int       `json:"count"`
	FirstAt time.Time `json:"firstAt"`
	LastAt  time.Time `json:"lastAt"`
}

// FileRecord is a file a run had open (Write: for writing).
type FileRecord struct {
	Path  string `json:"path"`
	Write bool   `json:"write"`
	// OutsideWorkspace marks a write outside the run's workspace.
	OutsideWorkspace bool      `json:"outsideWorkspace,omitempty"`
	Command          string    `json:"command,omitempty"`
	Count            int       `json:"count"`
	FirstAt          time.Time `json:"firstAt"`
	LastAt           time.Time `json:"lastAt"`
}

// ConnRecord is a network connection a run opened (or, in precise mode,
// attempted).
type ConnRecord struct {
	Protocol string    `json:"protocol"`
	Remote   string    `json:"remote"`
	State    string    `json:"state,omitempty"`
	Command  string    `json:"command,omitempty"`
	Count    int       `json:"count"`
	FirstAt  time.Time `json:"firstAt"`
	LastAt   time.Time `json:"lastAt"`
}

// ActivitySupport says which kinds of activity the platform and mode can
// record.
type ActivitySupport struct {
	Commands    bool `json:"commands"`
	Files       bool `json:"files"`
	Connections bool `json:"connections"`
}

// ActivityDropped counts distinct records refused by the caps.
type ActivityDropped struct {
	Commands    int `json:"commands"`
	Files       int `json:"files"`
	Connections int `json:"connections"`
}

// ActivityRunRef names one recorded run.
type ActivityRunRef struct {
	RunID      string    `json:"runId"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	Live       bool      `json:"live,omitempty"`
}

// ActivityReport is one run's activity audit.
type ActivityReport struct {
	Identifier  string           `json:"identifier"`
	RunID       string           `json:"runId,omitempty"`
	Live        bool             `json:"live"`
	Mode        string           `json:"mode"`
	ModeReason  string           `json:"modeReason,omitempty"`
	Platform    string           `json:"platform"`
	Supports    ActivitySupport  `json:"supports"`
	Notes       []string         `json:"notes"`
	Workspace   string           `json:"workspace,omitempty"`
	Remote      bool             `json:"remote,omitempty"`
	StartedAt   time.Time        `json:"startedAt,omitzero"`
	UpdatedAt   time.Time        `json:"updatedAt,omitzero"`
	FinishedAt  time.Time        `json:"finishedAt,omitzero"`
	Commands    []CommandRecord  `json:"commands"`
	Files       []FileRecord     `json:"files"`
	Connections []ConnRecord     `json:"connections"`
	Dropped     ActivityDropped  `json:"dropped"`
	Runs        []ActivityRunRef `json:"runs,omitempty"`
}

// activityLog is one run's bounded, deduplicated activity. Guarded by
// Monitor.mu.
type activityLog struct {
	commands []CommandRecord
	files    []FileRecord
	conns    []ConnRecord
	cmdIdx   map[string]int
	fileIdx  map[string]int
	connIdx  map[string]int
	dropped  ActivityDropped
	updated  time.Time
	dirty    bool
}

func newActivityLog() *activityLog {
	return &activityLog{cmdIdx: map[string]int{}, fileIdx: map[string]int{}, connIdx: map[string]int{}}
}

func (a *activityLog) touch(at time.Time) {
	a.updated, a.dirty = at, true
}

// addCommand records a started command (name and args already redacted).
func (a *activityLog) addCommand(at time.Time, pid int, name, args string) {
	if name == "" {
		return
	}
	key := name + "\x00" + args
	if i, ok := a.cmdIdx[key]; ok {
		a.commands[i].Count++
		a.commands[i].LastAt, a.commands[i].PID = at, pid
		a.touch(at)
		return
	}
	if len(a.commands) >= maxActivityCommands {
		a.dropped.Commands++
		a.touch(at)
		return
	}
	a.cmdIdx[key] = len(a.commands)
	a.commands = append(a.commands, CommandRecord{Name: name, Args: args, PID: pid, Count: 1, FirstAt: at, LastAt: at})
	a.touch(at)
}

// addFile records an open file. A path seen again counts again only when
// it is seen by a later sample or event (callers dedupe per sample); a
// write upgrades a read.
func (a *activityLog) addFile(at time.Time, path string, write bool, command string) {
	path = boundUTF8(logging.RedactString(path), pathMaxBytes)
	if path == "" {
		return
	}
	if i, ok := a.fileIdx[path]; ok {
		f := &a.files[i]
		f.Count++
		f.LastAt = at
		if write && !f.Write {
			f.Write, f.Command = true, command
		}
		a.touch(at)
		return
	}
	if len(a.files) >= maxActivityFiles {
		a.dropped.Files++
		a.touch(at)
		return
	}
	a.fileIdx[path] = len(a.files)
	a.files = append(a.files, FileRecord{Path: path, Write: write, Command: command, Count: 1, FirstAt: at, LastAt: at})
	a.touch(at)
}

// addConn records a connection to remote.
func (a *activityLog) addConn(at time.Time, protocol, remote, state, command string) {
	if remote == "" {
		return
	}
	key := protocol + " " + remote
	if i, ok := a.connIdx[key]; ok {
		c := &a.conns[i]
		c.Count++
		c.LastAt = at
		if state != "" {
			c.State = state
		}
		a.touch(at)
		return
	}
	if len(a.conns) >= maxActivityConnections {
		a.dropped.Connections++
		a.touch(at)
		return
	}
	a.connIdx[key] = len(a.conns)
	a.conns = append(a.conns, ConnRecord{Protocol: protocol, Remote: remote, State: state, Command: command, Count: 1, FirstAt: at, LastAt: at})
	a.touch(at)
}

// counts returns how many commands, written files and connections the log
// holds.
func (a *activityLog) counts() (commands, written, conns int) {
	for _, f := range a.files {
		if f.Write {
			written++
		}
	}
	return len(a.commands), written, len(a.conns)
}

// fill copies the log into r, marking writes outside workspace.
func (a *activityLog) fill(r *ActivityReport, workspace string) {
	r.Commands = slices.Clone(a.commands)
	r.Files = slices.Clone(a.files)
	r.Connections = slices.Clone(a.conns)
	r.Dropped = a.dropped
	r.UpdatedAt = a.updated
	for i := range r.Files {
		r.Files[i].OutsideWorkspace = r.Files[i].Write && workspace != "" && !insideDir(workspace, r.Files[i].Path)
	}
}

// insideDir reports whether path is dir or below it.
func insideDir(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// platformNotes explains what the mode and platform can and cannot see.
func platformNotes(goos, mode string, remote bool) (ActivitySupport, []string) {
	notes := []string{}
	if remote {
		notes = append(notes, "This run executes on an SSH worker host: only the local ssh client is visible here.")
	}
	switch mode {
	case ModeOff:
		return ActivitySupport{}, append(notes, "Activity recording is off (agent.activity_audit: off).")
	case ModePrecise:
		return ActivitySupport{Commands: true, Files: true, Connections: true},
			append(notes, "Precise: every exec, open-for-write and connect of the run's process tree, from eBPF tracepoints. Files opened read-only are not listed.")
	}
	switch goos {
	case "linux":
		return ActivitySupport{Commands: true, Files: true, Connections: true},
			append(notes, "Sampled every 2s from /proc: short-lived processes, files and connections between two samples may be missed.")
	case "darwin":
		return ActivitySupport{Commands: true, Connections: true},
			append(notes, "Sampled every 2s: short-lived activity may be missed.",
				"File activity is not available on macOS: it needs the Endpoint Security entitlement.")
	case "windows":
		return ActivitySupport{Commands: true, Connections: true},
			append(notes, "Sampled every 2s: short-lived activity may be missed.",
				"File activity is not available on Windows.")
	}
	return ActivitySupport{Commands: true}, append(notes, "Sampled every 2s: short-lived activity may be missed.")
}
