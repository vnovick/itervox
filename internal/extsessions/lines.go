package extsessions

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
)

// Bounds on how much of a transcript one read looks at. Transcripts grow to
// tens of megabytes; discovery reads only the head (metadata) and the tail
// (title), and a transcript page reads backwards from a cursor.
const (
	// headScanBytes bounds the head read for a session's metadata.
	headScanBytes = 256 * 1024
	// tailScanBytes bounds the tail read for a session's title.
	tailScanBytes = 256 * 1024
	// maxRecordBytes skips a single line longer than this (a huge tool
	// result) instead of buffering it.
	maxRecordBytes = 1024 * 1024
)

// headLines calls fn for each complete line within the first limit bytes of
// path, until fn returns false. Lines over maxRecordBytes are skipped.
func headLines(path string, limit int64, fn func(line []byte) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(io.LimitReader(f, limit), 64*1024)
	var long bool
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			if !long {
				buf = append(buf, chunk...)
				if len(buf) > maxRecordBytes {
					long, buf = true, nil
				}
			}
			continue
		case err != nil && !errors.Is(err, io.EOF):
			return err
		}
		if errors.Is(err, io.EOF) {
			return nil // a trailing partial line may still be being written
		}
		if !long {
			line := append(buf, chunk...)
			if !fn(bytes.TrimSpace(line)) {
				return nil
			}
		}
		long, buf = false, nil
	}
}

// tailLines reads path backwards from offset end (exclusive; < 0 or past
// the end of the file: the end of the file) and calls fn for each complete
// line, newest first, with the byte offset the line starts at. It stops when
// fn returns false or after scanning about limit bytes, and returns the
// offset reading stopped at: the end of the line fn refused (the start of
// the last line it took), the end of the oldest line not yet reported once
// the limit is hit, or 0 at the start of the file. Passing that offset back
// as end continues where it left off, starting with the refused line.
func tailLines(path string, end, limit int64, fn func(line []byte, start int64) bool) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if end < 0 || end > st.Size() {
		end = st.Size()
	}
	const chunkSize = 64 * 1024
	pos := end
	taken := end      // start of the oldest line fn took
	var carry []byte  // bytes after the last newline seen: the line being assembled
	skipping := false // the line being assembled is over maxRecordBytes
	scanned := int64(0)
	for pos > 0 && scanned < limit {
		n := min(int64(chunkSize), pos)
		pos -= n
		scanned += n
		chunk := make([]byte, n)
		if _, err := f.ReadAt(chunk, pos); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		data := append(chunk, carry...)
		for {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 {
				break
			}
			line := data[i+1:]
			if skipping {
				skipping = false
			} else if len(line) > 0 {
				start := pos + int64(i) + 1
				if !fn(bytes.TrimSpace(line), start) {
					return taken, nil
				}
				taken = start
			}
			data = data[:i]
		}
		carry = data
		if len(carry) > maxRecordBytes {
			carry, skipping = nil, true
		}
	}
	if pos > 0 {
		return pos + int64(len(carry)), nil
	}
	if len(carry) > 0 && !skipping && !fn(bytes.TrimSpace(carry), 0) {
		return taken, nil
	}
	return 0, nil
}

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}
