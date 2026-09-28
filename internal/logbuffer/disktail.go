package logbuffer

// Per-issue log files on disk: tail reads, line counting, and size-capped
// rotation (CORE-036).
//
// A file is capped at the writer's fileCap (maxLogFileBytes, 20 MiB). An
// append that would push it past the cap first renames it to <name>.log.1 —
// replacing any older rotated file — and starts a new <name>.log, so one
// issue keeps at most two files (<= 2 x cap on disk). Rotation happens only on
// the disk writer goroutine, between whole lines, so no line is ever split
// across the boundary and no other goroutine can observe a half-rotated file.
//
// Sequence numbering is unaffected by rotation: the writer's fileState.offset
// maps the CURRENT file's line k to the issue's local number offset+k, and a
// rotation advances offset by the rotated file's line count. A window read
// across the boundary is therefore numbered contiguously, and a cursor that
// has fallen out of the retained window is caught by GetSince's ordinary
// cursor < base check — a gap, never a silent skip.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync/atomic"
)

// maxLogFileBytes caps one per-issue log file; see the file comment.
const maxLogFileBytes = 20 << 20

// tailReadChunk is the tail reader's read size: it reads backwards in chunks
// of this size, so it consumes at most one chunk beyond the lines it returns.
// A var only so a same-package property test can use tiny chunks and put
// newlines exactly on chunk boundaries; production never changes it.
var tailReadChunk = 64 << 10

// tailScanCeiling bounds how far back the tail reader scans for line starts:
// maxLinesPerIssue lines of at most maxLineBytes each. Only a file written
// before Add capped line length can hold a line that reaches it.
const tailScanCeiling = maxLinesPerIssue * maxLineBytes

// rotatedSuffix names the one rotated generation kept per issue file.
const rotatedSuffix = ".1"

// logReadFile is what the tail reader reads through; openLogRead is a seam so
// a same-package test can count the bytes a read consumes.
type logReadFile interface {
	io.Reader
	io.ReaderAt
	Close() error
}

var openLogRead = func(p string) (logReadFile, error) { return os.Open(p) }

// readFromDisk returns identifier's newest maxLinesPerIssue lines, newest
// last: the tail of its current file and, when that file holds fewer lines,
// the tail of its rotated file for the rest. It never reads a whole file —
// only the returned lines plus at most one tailReadChunk per file (CORE-036;
// it used to os.ReadFile the entire file on every empty-window read). Lines
// longer than maxLineBytes are truncated exactly as Add truncates them. ok is
// false when the current file is missing or unreadable.
//
// The caller supplies the numbering (fileState): the tail reader does not
// count the file, which is what keeps it tail-only.
func readFromDisk(dir, identifier string) (lines []string, ok bool) {
	p := issuePath(dir, identifier)
	lines, atStart, ok := tailLines(p, maxLinesPerIssue)
	if atStart && len(lines) < maxLinesPerIssue {
		older, _, _ := tailLines(p+rotatedSuffix, maxLinesPerIssue-len(lines))
		lines = append(older, lines...)
	}
	return lines, ok
}

// tailLines returns the last n lines of the file at p (newest last), using
// countDiskLines' definition of a line: every '\n' ends one, and a trailing
// partial record is one more. atStart reports that the whole file was
// consumed, i.e. it holds no line older than the first one returned. A line
// whose start lies beyond tailScanCeiling is dropped; if not even the newest
// line starts within it, a single truncation notice stands in for it.
func tailLines(p string, n int) (lines []string, atStart, ok bool) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, true, false
	}
	size := st.Size()
	if size == 0 || n <= 0 {
		return nil, true, true
	}
	f, err := openLogRead(p)
	if err != nil {
		return nil, true, false
	}
	defer func() { _ = f.Close() }()

	var chunks [][]byte // newest first
	pos := size         // bytes [pos, size) have been read
	limit := size       // end of the newest line's content (its '\n' excluded)
	start := int64(-1)  // offset where the oldest returned line starts
	earliest := int64(-1)
	seps := 0
	for pos > 0 && size-pos < tailScanCeiling {
		k := min(int64(tailReadChunk), pos)
		buf := make([]byte, k)
		if _, err := f.ReadAt(buf, pos-k); err != nil && !errors.Is(err, io.EOF) {
			slog.Warn("logbuffer: failed to read log file tail", "path", p, "error", err)
			return nil, true, false
		}
		if pos == size && buf[k-1] == '\n' {
			limit = size - 1 // the final '\n' ends the newest line
		}
		pos -= k
		chunks = append(chunks, buf)
		for i := k - 1; i >= 0; i-- {
			off := pos + i
			if off >= limit || buf[i] != '\n' {
				continue
			}
			seps++
			earliest = off
			if seps == n {
				start = off + 1
				break
			}
		}
		if start >= 0 {
			break
		}
	}
	switch {
	case start >= 0:
	case pos == 0:
		start, atStart = 0, true
	case earliest >= 0:
		start = earliest + 1 // the line before it starts beyond the ceiling
	default:
		// Not even the newest line starts within the ceiling.
		return []string{fmt.Sprintf("…[truncated: line longer than %d bytes]", tailScanCeiling)}, false, true
	}
	slices.Reverse(chunks)
	return splitChunks(chunks, pos, start, limit), atStart, true
}

// splitChunks returns the lines of bytes [start, limit) of the file, given
// its chunks in file order with the first one starting at offset pos. It is
// strings.Split(content, "\n") followed by truncateLine on every line, but it
// builds each line straight from the chunks and drops each chunk once split,
// so it never holds a joined copy of the content (M1-B1 fix round 1: the old
// Join + string conversion allocated about 3x the bytes read). A line is
// accumulated in one reused buffer of at most maxLineBytes; only the bytes
// truncateLine keeps are ever copied.
func splitChunks(chunks [][]byte, pos, start, limit int64) []string {
	var lines []string
	var cur []byte // the pending line's first bytes, at most maxLineBytes
	curLen := 0    // the pending line's full length
	emit := func() {
		if curLen <= maxLineBytes {
			lines = append(lines, string(cur))
		} else {
			end, suffix := truncation(curLen)
			lines = append(lines, string(cur[:end])+suffix)
		}
		cur, curLen = cur[:0], 0
	}
	add := func(part []byte) {
		curLen += len(part)
		if room := maxLineBytes - len(cur); room > 0 {
			cur = append(cur, part[:min(room, len(part))]...)
		}
	}
	off := pos
	for i, c := range chunks {
		lo, hi := max(start-off, 0), min(limit-off, int64(len(c)))
		off += int64(len(c))
		chunks[i] = nil // split once; let it go
		if lo >= hi {
			continue
		}
		seg := c[lo:hi]
		for {
			j := bytes.IndexByte(seg, '\n')
			if j < 0 {
				add(seg)
				break
			}
			add(seg[:j])
			emit()
			seg = seg[j+1:]
		}
	}
	emit() // the newest line: content ends at limit, before its '\n'
	return lines
}

// countFile streams the file at p and returns its line count and size.
func countFile(p string) (lines, size int64) {
	f, err := os.Open(p)
	if err != nil {
		return 0, 0
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 32<<10)
	last := byte('\n')
	for {
		k, rerr := f.Read(buf)
		if k > 0 {
			lines += int64(bytes.Count(buf[:k], []byte{'\n'}))
			size += int64(k)
			last = buf[k-1]
		}
		if rerr != nil {
			break
		}
	}
	if last != '\n' {
		lines++ // trailing partial record
	}
	return lines, size
}

// syncFileState re-counts the current file when its size is not what the
// writer last wrote — something other than this writer changed it (an
// external truncation or append, or a test seam standing in for the append).
// A file the writer alone maintains is never re-read. Writer goroutine only.
func syncFileState(fs *fileState, p string) {
	st, err := os.Stat(p)
	if err != nil {
		fs.lines, fs.size = 0, 0
		return
	}
	if st.Size() != fs.size {
		fs.lines, fs.size = countFile(p)
	}
}

// removeIssueFiles deletes identifier's rotated file, then its current file,
// treating "already gone" as success. The rotated file goes first so a failure
// leaves the current file — and the numbering it anchors — intact.
func removeIssueFiles(dir, identifier string) error {
	p := issuePath(dir, identifier)
	if err := removeLogFile(p + rotatedSuffix); err != nil {
		return err
	}
	return removeLogFile(p)
}

// appendRotating appends lines, in order, to identifier's current file,
// rotating it first whenever the next line would push it past capBytes (and
// the file is not empty, so a single oversized line cannot rotate forever).
// fs tracks the current file's line count and size and, on rotation, its
// offset. Writer goroutine only. A failed rotation is logged and the append
// continues in the current file; a failed write is returned (the lines stay
// in memory, and the next read re-syncs fs from the file).
func appendRotating(fs *fileState, dir, identifier string, lines []string, capBytes int64, rotations *atomic.Int64) error {
	p := issuePath(dir, identifier)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("logbuffer: failed to create log dir", "dir", dir, "error", err)
		return err
	}
	syncFileState(fs, p)
	f, err := openAppend(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	canRotate := true
	for _, line := range lines {
		rec := int64(len(line) + 1)
		if canRotate && fs.size > 0 && fs.size+rec > capBytes {
			_ = f.Close()
			if rerr := os.Rename(p, p+rotatedSuffix); rerr != nil {
				slog.Warn("logbuffer: failed to rotate log file; appending past the cap", "path", p, "error", rerr)
				canRotate = false
			} else {
				fs.offset += fs.lines
				fs.lines, fs.size = 0, 0
				rotations.Add(1)
			}
			if f, err = openAppend(p); err != nil {
				return err
			}
		}
		if _, err := f.WriteString(line + "\n"); err != nil {
			slog.Warn("logbuffer: failed to write log line", "path", p, "error", err)
			return err
		}
		fs.size += rec
		fs.lines++
	}
	return nil
}

func openAppend(p string) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("logbuffer: failed to open log file", "path", p, "error", err)
	}
	return f, err
}
