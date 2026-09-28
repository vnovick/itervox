package agent

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTailWriterNeverExceedsCap is CORE-028's bounded-memory acceptance: 10
// MiB written through the writer in 4 KiB chunks never leaves more than 64
// KiB buffered after any Write, and the final content is exactly the last 64
// KiB plus the truncation marker. A buffer-everything-then-truncate writer
// fails the per-Write bound even though its final String() would pass.
func TestTailWriterNeverExceedsCap(t *testing.T) {
	const total, chunk = 10 << 20, 4 << 10
	w := newTailBuffer(maxStderrCaptureBytes)
	var all bytes.Buffer // the reference stream, kept only by the test
	p := make([]byte, chunk)
	for off := 0; off < total; off += chunk {
		for i := range p {
			p[i] = byte('a' + (off/chunk+i)%26)
		}
		n, err := w.Write(p)
		if err != nil || n != len(p) {
			t.Fatalf("Write at offset %d = (%d, %v), want (%d, nil)", off, n, err, len(p))
		}
		if got := w.Len(); got > maxStderrCaptureBytes {
			t.Fatalf("after writing %d bytes the writer buffers %d, cap is %d", off+chunk, got, maxStderrCaptureBytes)
		}
		all.Write(p)
	}
	want := failureTextTruncationMarker + string(all.Bytes()[all.Len()-maxStderrCaptureBytes:])
	if got := w.String(); got != want {
		t.Fatalf("final content: len %d, want the last %d bytes plus the marker (len %d); prefix %q",
			len(got), maxStderrCaptureBytes, len(want), got[:min(len(got), 40)])
	}
}

// TestTailWriterEdgeCases pins the branches the chunked row does not reach:
// an under-cap stream is returned verbatim with no marker, a single Write
// larger than the cap keeps its own tail, and a cut through a multi-byte
// rune does not leave an invalid leading byte.
func TestTailWriterEdgeCases(t *testing.T) {
	w := newTailBuffer(8)
	_, _ = w.Write([]byte("abc"))
	_, _ = w.Write([]byte("defgh"))
	if got := w.String(); got != "abcdefgh" {
		t.Fatalf("exactly-at-cap stream = %q, want it verbatim with no marker", got)
	}

	w = newTailBuffer(8)
	_, _ = w.Write([]byte("0123456789ABCDEF"))
	if got := w.String(); got != failureTextTruncationMarker+"89ABCDEF" {
		t.Fatalf("single oversized Write = %q", got)
	}

	w = newTailBuffer(5)
	_, _ = w.Write([]byte("x" + "é" + "abcd")) // é is 2 bytes; the 5-byte tail starts mid-rune
	got := w.String()
	if !utf8.ValidString(got) || got != failureTextTruncationMarker+"abcd" {
		t.Fatalf("mid-rune cut = %q, want the marker plus %q", got, "abcd")
	}
}

// TestFormatFailureTextTruncatesStderr is CORE-028's named FailureText row:
// more than 1 MiB of stderr yields a FailureText of at most 64 KiB that
// carries the truncation marker and keeps the stderr tail, with the
// agent-reported failure still leading it.
func TestFormatFailureTextTruncatesStderr(t *testing.T) {
	const sentinel = "ROOT-CAUSE: quota exhausted"
	stderr := strings.Repeat("noise line\n", (2<<20)/11) + sentinel

	for _, tc := range []struct{ name, agentFailure string }{
		{"stderr_only", ""},
		{"with_agent_failure", "turn.failed: usage limit"},
		{"with_oversized_agent_failure", "HEAD-OF-FAILURE " + strings.Repeat("r", 1<<20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := formatFailureText(tc.agentFailure, stderr, errors.New("exit status 1"))
			if len(got) > maxFailureTextBytes {
				t.Fatalf("FailureText is %d bytes, cap is %d", len(got), maxFailureTextBytes)
			}
			if !strings.Contains(got, failureTextTruncationMarker) {
				t.Fatal("a truncated FailureText must carry the truncation marker")
			}
			if !strings.HasSuffix(got, sentinel) {
				t.Fatalf("the stderr tail must be preserved; FailureText ends %q", got[len(got)-40:])
			}
			if tc.agentFailure != "" && !strings.HasPrefix(got, tc.agentFailure[:16]) {
				t.Fatalf("the agent-reported failure must still lead FailureText; got prefix %q", got[:40])
			}
		})
	}

	// M3: 3- and 4-byte runes straddling both cuts. Each shift moves the
	// stderr tail cut and the agent-failure head cut through every byte
	// offset of a rune, so without the rune-boundary loops at least one
	// shift yields invalid UTF-8 at a cut.
	t.Run("multibyte_runes_straddling_the_cuts", func(t *testing.T) {
		for _, r := range []string{"日", "🚀"} { // 3 and 4 bytes
			for shift := range len(r) {
				stderr := strings.Repeat("x", shift) + strings.Repeat(r, (2*maxFailureTextBytes)/len(r)) + sentinel
				agentFailure := strings.Repeat("a", shift) + strings.Repeat(r, (2*maxAgentFailureBytes)/len(r))
				for _, af := range []string{"", agentFailure} {
					got := formatFailureText(af, stderr, nil)
					if !utf8.ValidString(got) {
						t.Fatalf("rune %q shift %d agentFailure=%v: FailureText is not valid UTF-8 at a cut", r, shift, af != "")
					}
					if len(got) > maxFailureTextBytes {
						t.Fatalf("rune %q shift %d: %d bytes over cap", r, shift, len(got))
					}
					if !strings.HasSuffix(got, sentinel) {
						t.Fatalf("rune %q shift %d: tail lost", r, shift)
					}
				}
			}
		}
	})

	// Under the cap nothing changes (the pre-CORE-028 shape, byte for byte).
	if got := formatFailureText("boom", "  err line \n", nil); got != "boom | stderr: err line" {
		t.Fatalf("small FailureText changed: %q", got)
	}
}
