package agent

import (
	"sync"
	"unicode/utf8"
)

// maxStderrCaptureBytes bounds how much of an agent subprocess's stderr a
// runner keeps for FailureText (CORE-028). Only the most recent bytes are
// kept: the root cause of a failure is almost always at the end of the
// stream, after whatever noise preceded it.
const maxStderrCaptureBytes = 64 << 10

// tailBuffer is an io.Writer that retains only the last max bytes written to
// it, so a subprocess that floods stderr for the whole turn (up to the hard
// turn timeout, or forever when agent.turn_timeout_ms <= 0 disables it) can
// never grow the daemon's memory past max. It replaces the unbounded
// bytes.Buffer the runners used to assign to cmd.Stderr (CORE-028).
//
// exec.Cmd copies stderr into it from its own goroutine; the runners read it
// only after cmd.Wait has joined that goroutine. The mutex makes any other
// access pattern safe as well.
type tailBuffer struct {
	mu        sync.Mutex
	max       int
	buf       []byte
	truncated bool
}

func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{max: max}
}

// Write keeps the last b.max bytes of everything written so far. It never
// fails and always reports len(p) written, so the copying goroutine keeps
// draining the pipe instead of blocking the child.
func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max <= 0 {
		b.truncated = b.truncated || len(p) > 0
		return len(p), nil
	}
	if len(p) >= b.max {
		// p alone fills the window: everything retained so far, and p's own
		// head beyond max, is dropped.
		if len(p) > b.max || len(b.buf) > 0 {
			b.truncated = true
		}
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return len(p), nil
	}
	if over := len(b.buf) + len(p) - b.max; over > 0 {
		n := copy(b.buf, b.buf[over:])
		b.buf = b.buf[:n]
		b.truncated = true
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// Len reports how many bytes are currently retained (always <= max).
func (b *tailBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.buf)
}

// String returns the retained tail. When earlier bytes were dropped it is
// prefixed with failureTextTruncationMarker, and a UTF-8 sequence split by
// the cut is skipped so the result starts on a rune boundary.
func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.truncated {
		return string(b.buf)
	}
	tail := b.buf
	for i := 0; i < len(tail) && i < utf8.UTFMax && !utf8.RuneStart(tail[0]); i++ {
		tail = tail[1:]
	}
	return failureTextTruncationMarker + string(tail)
}
