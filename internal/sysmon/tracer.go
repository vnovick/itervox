package sysmon

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Precise-mode event kinds (the eBPF program's event header).
const (
	preciseExec    = 1
	preciseOpen    = 2
	preciseConnect = 3
)

// preciseEvent is one decoded event of the precise tracer.
type preciseEvent struct {
	Kind int
	PID  uint32
	Slot uint32
	// Path is the exec'd file or the opened path (as the process passed
	// it: a relative path is relative to its cwd).
	Path string
	// Protocol / Remote describe a connect.
	Protocol string
	Remote   string
}

// activityTracer is precise mode's kernel side: it follows the process
// trees of tracked pids and reports their exec, open-for-write and connect
// calls through the deliver callback it was opened with.
type activityTracer interface {
	// track adds pid (and every process it forks from now on) to slot.
	track(pid, slot uint32) error
	// untrack forgets every process of slot.
	untrack(slot uint32)
	close() error
}

// preciseNamesCap bounds the per-pid name cache of precise events; past it
// the cache starts over.
const preciseNamesCap = 4096

// openTracer opens precise mode's tracer; a seam so tests can force the
// fallback. ebpf_linux.go / ebpf_other.go provide the default.
var openTracer = openPreciseTracer

// startPrecise opens the tracer, or falls back to sampled mode with the
// reason.
func (m *Monitor) startPrecise() {
	t, err := openTracer(m.preciseEvent)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.mode, m.modeReason = ModeSampled, "precise mode unavailable: "+err.Error()
		slog.Warn("sysmon: precise activity mode unavailable; sampling instead", "reason", err.Error())
		return
	}
	m.tracerMu.Lock()
	m.tracer = t
	m.tracerMu.Unlock()
	m.mode, m.modeReason = ModePrecise, ""
	// Groups that started before the tracer was ready.
	for _, e := range m.runs {
		for pid := range e.leaders {
			_ = t.track(uint32(pid), e.slot) //nolint:gosec // pids fit in uint32
		}
	}
	slog.Info("sysmon: precise activity mode on (eBPF)")
}

func (m *Monitor) stopPrecise() {
	m.tracerMu.Lock()
	defer m.tracerMu.Unlock()
	if m.tracer != nil {
		if err := m.tracer.close(); err != nil {
			slog.Debug("sysmon: closing the precise tracer", "error", err)
		}
		m.tracer = nil
	}
}

// preciseEvent records one tracer event on its run. Called on the tracer's
// reader goroutine only; reads /proc before taking the lock. An exec reads
// the new program's command line; an open or connect only needs the
// process's name, cached per pid (writes can come thousands a second).
func (m *Monitor) preciseEvent(ev preciseEvent) {
	pidDir := filepath.Join(m.procRoot, strconv.Itoa(int(ev.PID)))
	p := procInfo{PID: int(ev.PID)}
	if ev.Kind == preciseExec {
		p.Comm = filepath.Base(ev.Path)
	}
	if m.preciseNames == nil || len(m.preciseNames) > preciseNamesCap {
		m.preciseNames = map[uint32][2]string{}
	}
	cached, known := m.preciseNames[ev.PID]
	if ev.Kind == preciseExec || !known {
		if b, err := os.ReadFile(filepath.Join(pidDir, "cmdline")); err == nil {
			p.Argv = parseCmdline(b)
		}
		if ev.Kind != preciseExec {
			if b, err := os.ReadFile(filepath.Join(pidDir, "comm")); err == nil {
				p.Comm = strings.TrimRight(string(b), "\r\n")
			}
		}
		name, args := commandLine(&p)
		cached = [2]string{name, args}
		m.preciseNames[ev.PID] = cached
	}
	name, args := cached[0], cached[1]
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.slots[ev.Slot]
	if e == nil {
		return
	}
	switch ev.Kind {
	case preciseExec:
		e.activity.addCommand(now, p.PID, name, args)
	case preciseOpen:
		path := ev.Path
		if !filepath.IsAbs(path) {
			if cwd, err := os.Readlink(filepath.Join(pidDir, "cwd")); err == nil {
				path = filepath.Join(cwd, path)
			}
		}
		if path, ok := regularPath(path); ok {
			e.activity.addFile(now, path, true, name)
		}
	case preciseConnect:
		e.activity.addConn(now, ev.Protocol, ev.Remote, "", name)
	}
}

// regularPath keeps paths outside /dev, /proc and /sys.
func regularPath(p string) (string, bool) {
	for _, pre := range []string{"/dev/", "/proc/", "/sys/"} {
		if strings.HasPrefix(p, pre) {
			return "", false
		}
	}
	return p, p != ""
}
