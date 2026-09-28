package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// handlers_logstream.go holds the per-issue live log SSE handler
// (GET /api/v1/issues/{identifier}/log-stream) and its poll-ticker seam.
// Moved verbatim out of handlers.go to keep that file under its size budget;
// no behaviour change.

// logStreamTicker abstracts the poll ticker used by handleIssueLogStream so
// tests can drive its loop deterministically (a hand-fed channel) instead of
// racing a real 500ms wall-clock ticker across multiple batches/ticks.
type logStreamTicker interface {
	C() <-chan time.Time
	Stop()
}

type realLogStreamTicker struct{ t *time.Ticker }

func (r realLogStreamTicker) C() <-chan time.Time { return r.t.C }
func (r realLogStreamTicker) Stop()               { r.t.Stop() }

// newLogStreamTicker is a package var purely so internal/server tests can
// substitute a fake ticker; production always gets the real 500ms ticker.
var newLogStreamTicker = func() logStreamTicker {
	return realLogStreamTicker{time.NewTicker(500 * time.Millisecond)}
}

// handleIssueLogStream streams parsed log entries for one issue as SSE.
// It resumes by sequence number rather than buffer position (CORE-003): the
// SSE "id:" line carries "<epoch>-<seq>" from logbuffer.Buffer.GetSince, so a
// reconnecting client (Last-Event-ID) is resumed exactly where it left off
// even after the underlying 500-line ring buffer has slid forward past that
// point — instead of the old positional cursor going permanently stale once
// it exceeded the buffer's length. When the client's cursor has fallen out
// of the retained window, carries a foreign epoch (a daemon restart, or a
// pre-CORE-003 Last-Event-ID), or the current window came from the on-disk
// fallback file (whose numbering has no relation to any prior in-memory
// cursor), the client is sent one "event: gap" frame — stamped with the seq
// immediately before the replay so a disconnect between the gap frame and
// the replay resumes the same replay rather than skipping it — followed by
// a full replay of the current window, rather than silently resuming as if
// nothing was missed. See internal/logbuffer.Buffer.GetSince's doc comment
// for the full sequence contract, including the Clear/Remove/disk-fallback/
// rotation semantics.
// GET /api/v1/issues/{identifier}/log-stream
func (s *Server) handleIssueLogStream(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")

	flusher, ok := beginSSE(w)
	if !ok {
		return
	}

	// On reconnect we honor the Last-Event-ID header (T-18) so the client
	// resumes after the last event it acknowledged. Browsers (and the
	// @microsoft/fetch-event-source library used by web/) automatically
	// echo this header on reconnect when the server emits "id:" lines.
	epoch, cursor, hasCursor := parseLogStreamCursor(r.Header.Get("Last-Event-ID"))

	sendNew := func() bool {
		lines, curEpoch, next, gap := s.client.GetSince(r.Context(), identifier, epoch, cursor, hasCursor)
		// seq starts at the sequence of the first raw line in this batch;
		// every raw line (even a skipped/unparseable one) still consumes a
		// seq value so later ids stay correctly numbered.
		seq := next - int64(len(lines))
		if gap {
			// The gap frame's id is the seq immediately BEFORE the replay
			// that follows (not `next`, the seq of its last line): if the
			// connection drops after this frame but before any of the
			// replay's "event: log" frames land, a reconnect echoing this
			// id must land back at the start of the very same replay
			// (win[cursor-base:] with cursor==base returns the whole
			// window again) rather than at `next`, where GetSince would
			// see the client as already caught up and silently withhold
			// the window it was just promised.
			if err := writeSSEFrame(w, "id: %d-%d\nevent: gap\ndata: {}\n\n", curEpoch, seq); err != nil {
				return false
			}
		}
		for _, line := range lines {
			seq++
			entry, skip := parseLogLine(line)
			if skip {
				continue
			}
			b, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			// id: <epoch>-<seq> lets the client resume from exactly this
			// point on reconnect via the Last-Event-ID header.
			if err := writeSSEFrame(w, "id: %d-%d\nevent: log\ndata: %s\n\n", curEpoch, seq, b); err != nil {
				return false
			}
		}
		epoch, cursor, hasCursor = curEpoch, next, true
		flushSSE(w, flusher)
		return true
	}

	if !sendNew() {
		return
	}

	ticker := newLogStreamTicker()
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.streamsDone: // generation shut down (CORE-025); the client resumes via Last-Event-ID
			return
		case <-ticker.C():
			if !sendNew() {
				return
			}
		}
	}
}
