// Package logbuffer provides a per-identifier ring buffer for recent log lines,
// shared between worker loggers and the terminal status UI.
package logbuffer

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxLinesPerIssue = 500

// issueBuf is a per-identifier ring buffer with its own mutex.
// Different identifiers never contend with each other.
type issueBuf struct {
	mu    sync.RWMutex
	lines []string
}

// Buffer stores the last N log lines per issue identifier.
// When a log directory is configured via SetLogDir, lines are also appended to
// per-issue files on disk so they survive restarts and issue completion.
type Buffer struct {
	issues sync.Map // map[string]*issueBuf

	// dirMu guards logDir only. It is separate from per-issue locks
	// so reading logDir never blocks on a different issue's Add.
	dirMu  sync.RWMutex
	logDir string // empty = no disk persistence
}

// New creates an empty Buffer.
func New() *Buffer {
	return &Buffer{}
}

// SetLogDir configures a directory for per-issue log file persistence.
// The directory is created on first use. Calling this after Add calls is safe.
func (b *Buffer) SetLogDir(dir string) {
	b.dirMu.Lock()
	b.logDir = dir
	b.dirMu.Unlock()
}

func (b *Buffer) getLogDir() string {
	b.dirMu.RLock()
	defer b.dirMu.RUnlock()
	return b.logDir
}

// getOrCreate returns the issueBuf for identifier, creating it if needed.
func (b *Buffer) getOrCreate(identifier string) *issueBuf {
	if v, ok := b.issues.Load(identifier); ok {
		return v.(*issueBuf)
	}
	ib := &issueBuf{}
	v, _ := b.issues.LoadOrStore(identifier, ib)
	return v.(*issueBuf)
}

// maxLineBytes caps the on-disk and in-memory size of a single log line. A
// pathological agent can emit a 100+MB single line (e.g. dumping a binary as
// hex), which then pins maxLinesPerIssue × that-size in RAM and blocks all
// readers behind serialization. 64KiB is generous for human-readable output
// and keeps worst-case memory at ~32MB per issue (was ~500MB).
const maxLineBytes = 64 * 1024

// Add appends a line for the given identifier, dropping the oldest if over capacity.
// If a log directory is configured, the line is also written to disk.
// Lines longer than maxLineBytes are truncated with a trailing notice (T-11).
func (b *Buffer) Add(identifier, line string) {
	if len(line) > maxLineBytes {
		suffix := fmt.Sprintf("…[truncated %d bytes]", len(line)-maxLineBytes)
		// G-20 (gaps_280426_2): clamp with max(0, …) so a degenerate config
		// where the suffix exceeds maxLineBytes (e.g. tests using a tiny cap)
		// does not produce a negative slice index. At the production
		// maxLineBytes (64 KiB) the suffix is always tiny; this guard is a
		// belt-and-suspenders for future tunings.
		end := min(max(0, maxLineBytes-len(suffix)), len(line))
		line = line[:end] + suffix
	}
	ib := b.getOrCreate(identifier)
	ib.mu.Lock()
	ib.lines = append(ib.lines, line)
	if len(ib.lines) > maxLinesPerIssue {
		ib.lines = ib.lines[len(ib.lines)-maxLinesPerIssue:]
	}
	ib.mu.Unlock()

	if dir := b.getLogDir(); dir != "" {
		appendToDisk(dir, identifier, line)
	}
}

// Get returns a snapshot of recent lines for the given identifier (newest last).
// If the in-memory buffer is empty and a log directory is configured, falls back
// to reading the on-disk log file.
func (b *Buffer) Get(identifier string) []string {
	dir := b.getLogDir()

	v, ok := b.issues.Load(identifier)
	if !ok {
		if dir == "" {
			return nil
		}
		return readFromDisk(dir, identifier)
	}

	ib := v.(*issueBuf)
	ib.mu.RLock()
	n := len(ib.lines)
	if n == 0 {
		ib.mu.RUnlock()
		if dir == "" {
			return nil
		}
		return readFromDisk(dir, identifier)
	}
	out := make([]string, n)
	copy(out, ib.lines)
	ib.mu.RUnlock()
	return out
}

// Identifiers returns all identifiers that have log data — either in-memory or
// on disk. The returned slice is unsorted.
func (b *Buffer) Identifiers() []string {
	dir := b.getLogDir()
	seen := make(map[string]struct{})
	var ids []string

	b.issues.Range(func(key, _ any) bool {
		id := key.(string)
		ids = append(ids, id)
		seen[id] = struct{}{}
		return true
	})

	if dir == "" {
		return ids
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ids
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".log")
		// reverse the filename sanitisation applied in issuePath
		id = strings.NewReplacer("_", ":").Replace(id)
		if _, exists := seen[id]; !exists {
			ids = append(ids, id)
			seen[id] = struct{}{}
		}
	}
	return ids
}

// Remove deletes the in-memory buffer for the given identifier.
// The on-disk log file is intentionally preserved so logs remain viewable after
// an issue completes.
func (b *Buffer) Remove(identifier string) {
	b.issues.Delete(identifier)
}

// ClearAll deletes all in-memory buffers and all on-disk log files in logDir.
func (b *Buffer) ClearAll() error {
	b.issues.Range(func(key, _ any) bool {
		b.issues.Delete(key)
		return true
	})

	dir := b.getLogDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("logbuffer: read dir %s: %w", dir, err)
	}
	var first error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !os.IsNotExist(err) {
			if first == nil {
				first = err
			}
		}
	}
	return first
}

// Clear deletes both the in-memory buffer and the on-disk log file for identifier.
func (b *Buffer) Clear(identifier string) error {
	b.issues.Delete(identifier)
	if dir := b.getLogDir(); dir != "" {
		p := issuePath(dir, identifier)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// --- internal helpers ---

// issuePath returns the on-disk log file path for identifier within dir.
func issuePath(dir, identifier string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(identifier)
	return filepath.Join(dir, safe+".log")
}

// appendToDisk writes a single log line to the per-identifier file in dir.
func appendToDisk(dir, identifier, line string) {
	p := issuePath(dir, identifier)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("logbuffer: failed to create log dir", "dir", dir, "error", err)
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("logbuffer: failed to open log file", "path", p, "error", err)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line + "\n"); err != nil {
		slog.Warn("logbuffer: failed to write log line", "path", p, "error", err)
	}
}

func readFromDisk(dir, identifier string) []string {
	p := issuePath(dir, identifier)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > maxLinesPerIssue {
		lines = lines[len(lines)-maxLinesPerIssue:]
	}
	return lines
}
