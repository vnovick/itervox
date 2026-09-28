package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// CORE-007 — crash capture.
//
// An unrecovered panic or fatal runtime error on ANY goroutine reaches only
// stderr. Under the shipped systemd unit that is journald, but under the TUI
// the alt-screen hides it, and nothing lands in the rotating log file or
// HEARTBEAT.md. debug.SetCrashOutput hands the runtime a second descriptor it
// writes the full crash dump to, in addition to stderr.
//
// Lifecycle (all files live in the logs directory, outside the repo by
// default — ~/.itervox/logs/<kind>/<slug>):
//
//   - crash.log         0600, append-only; the runtime writes each crash dump
//     in full (it cannot be capped mid-write), so retention is bounded at
//     boot instead: when crash.log exceeds crashLogRotateBytes it is renamed
//     to crash.log.1 (one generation kept, the older .1 is replaced) before
//     SetCrashOutput is re-armed on a fresh crash.log.
//   - crash.log.marker  0600 JSON {size, mod_unix_nano} of crash.log as armed
//     by the current boot. The NEXT boot compares crash.log against it: any
//     change with a non-empty file means the previous run crashed, and that
//     boot reports it once (HEARTBEAT.md "- Last crash:" + one slog line).
//     The following boot sees an unchanged file and reports nothing, so an
//     old crash is never re-reported on every start.
//
// Privacy: the crash dump bypasses the RedactingHandler, and the known-secret
// redactor cannot vouch for arbitrary panic text, so HEARTBEAT.md carries only
// the crash timestamp and the crash.log path. The first panic line is logged
// once at boot through the default (redacting) slog handler.

const (
	crashLogName        = "crash.log"
	crashMarkerSuffix   = ".marker"
	crashLogRotateBytes = 1 << 20 // 1 MiB
	// crashFirstLineScan bounds how much of the new crash-dump bytes are read
	// to find the first "panic:" / "fatal error:" line.
	crashFirstLineScan = 64 << 10
	crashFirstLineMax  = 200
)

// crashReport describes a crash detected at boot. A zero At means none.
type crashReport struct {
	At   time.Time
	Path string
}

type crashMarker struct {
	Size        int64 `json:"size"`
	ModUnixNano int64 `json:"mod_unix_nano"`
}

// setCrashOutput is debug.SetCrashOutput; a package var only so tests can
// make arming fail (M0-close fix-G). Production never reassigns it.
var setCrashOutput = debug.SetCrashOutput

// armCrashOutput detects a crash left by the previous run, rotates and opens
// <logsDir>/crash.log, and arms debug.SetCrashOutput on it. Called once from
// main() after the default slog logger is installed.
func armCrashOutput(logsDir string) (crashReport, error) {
	report, f, err := prepareCrashLog(logsDir)
	if err != nil {
		return report, err
	}
	// SetCrashOutput duplicates the descriptor, so f can be closed at once.
	defer func() { _ = f.Close() }()
	if err := setCrashOutput(f, debug.CrashOptions{}); err != nil {
		return report, fmt.Errorf("crash: set crash output: %w", err)
	}
	return report, nil
}

// prepareCrashLog does everything armCrashOutput does except arming the
// runtime, so tests can drive the boot lifecycle without redirecting their
// own process's crash output. The returned file is open for append; the
// caller owns closing it.
func prepareCrashLog(logsDir string) (crashReport, *os.File, error) {
	path := filepath.Join(logsDir, crashLogName)
	markerPath := path + crashMarkerSuffix
	var report crashReport

	info, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		prev, hadMarker := readCrashMarker(markerPath)
		changed := !hadMarker || prev.Size != info.Size() || prev.ModUnixNano != info.ModTime().UnixNano()
		if info.Size() > 0 && changed {
			report = crashReport{At: info.ModTime().UTC(), Path: path}
			offset := int64(0)
			if hadMarker && prev.Size <= info.Size() {
				offset = prev.Size
			}
			// Read before any rotation below moves the file.
			firstLine := firstCrashLine(path, offset)
			if info.Size() > crashLogRotateBytes {
				// The rotation below moves this dump to crash.log.1 and
				// opens an empty crash.log, so the reported path — the one
				// HEARTBEAT.md names — must follow the dump (M0-close G8).
				report.Path = path + ".1"
			}
			slog.Warn("crash: the previous run crashed; full dump in crash log",
				"at", report.At.Format(time.RFC3339), "path", report.Path,
				"first_line", firstLine)
		}
		if info.Size() > crashLogRotateBytes {
			if err := os.Rename(path, path+".1"); err != nil {
				return report, nil, fmt.Errorf("crash: rotate %s: %w", path, err)
			}
		}
	case errors.Is(statErr, fs.ErrNotExist):
	default:
		return report, nil, fmt.Errorf("crash: stat %s: %w", path, statErr)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return report, nil, fmt.Errorf("crash: open %s: %w", path, err)
	}
	// OpenFile's mode only applies on create; tighten a pre-existing file.
	if err := f.Chmod(0o600); err != nil {
		slog.Warn("crash: chmod crash.log failed", "path", path, "error", err)
	}
	armed, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return report, nil, fmt.Errorf("crash: stat %s: %w", path, err)
	}
	data, _ := json.Marshal(crashMarker{Size: armed.Size(), ModUnixNano: armed.ModTime().UnixNano()})
	if err := atomicfs.WriteFile(markerPath, data, 0o600); err != nil {
		// Without a marker the next boot re-reports whatever crash.log holds;
		// annoying but safe, so this is not fatal.
		slog.Warn("crash: write crash marker failed", "path", markerPath, "error", err)
	}
	return report, f, nil
}

func readCrashMarker(path string) (crashMarker, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return crashMarker{}, false
	}
	var m crashMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return crashMarker{}, false
	}
	return m, true
}

// firstCrashLine returns the first "panic:" or "fatal error:" line at or after
// offset, truncated to crashFirstLineMax bytes. Only for the boot-time slog
// line (which the redacting handler filters); never for HEARTBEAT.md.
func firstCrashLine(path string, offset int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	sc := bufio.NewScanner(io.LimitReader(f, crashFirstLineScan))
	sc.Buffer(make([]byte, 0, 4096), crashFirstLineScan)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "panic:") || strings.HasPrefix(line, "fatal error:") {
			if len(line) > crashFirstLineMax {
				line = line[:crashFirstLineMax] + "…"
			}
			return line
		}
	}
	return ""
}
