package server

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/domain"
)

// handleSubLogStream streams parsed session/subagent log entries for one issue
// as SSE. The source data still comes from per-issue JSONL files, but the
// browser receives push updates instead of polling every few seconds.
// GET /api/v1/issues/{identifier}/sublog-stream
func (s *Server) handleSubLogStream(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")

	flusher, ok := sseFlusher(w)
	if !ok {
		return
	}

	initialEntries, err := s.client.FetchSubLogs(r.Context(), identifier)
	if err != nil {
		writeClientError(w, "fetch_failed", err)
		return
	}

	setSSEHeaders(w)

	// Resume-after-reconnect (T-18) by "<epoch>-<seq>" (CORE-153). The
	// entries are re-parsed from the session files on every tick, so a seq
	// is only a position; the epoch names WHICH set of files it is a
	// position in (subLogEpoch: a hash of the set's first entry, which an
	// append never changes and a replacement almost always does). A cursor
	// from another epoch — replaced or cleared session files, or a
	// pre-CORE-153 bare-number id — or past the end of the current set gets
	// one gap frame, "id: <epoch>-0", and a full replay, instead of resuming
	// mid-way into different content (the in-range aliasing M1-B1 left).
	cursorEpoch, cursorSeq, hasCursor := parseLogStreamCursor(r.Header.Get("Last-Event-ID"))
	epoch := subLogEpoch(initialEntries)
	sent := 0
	gapFirst := false
	switch {
	case !hasCursor:
	case cursorEpoch == epoch && cursorSeq >= 0 && cursorSeq <= int64(len(initialEntries)):
		sent = int(cursorSeq)
	case cursorEpoch == 0 && cursorSeq == 0:
		// The client saw only an empty set: nothing to announce.
	default:
		gapFirst = true
	}
	sendNew := func(entries []domain.IssueLogEntry) bool {
		cur := subLogEpoch(entries)
		if gapFirst || cur != epoch || sent > len(entries) {
			// A different set of session files (or a stale cursor): announce
			// the restart with a gap frame stamped with the seq before the
			// replay, as the issue-log stream does, so a client that keeps
			// lines across reconnects replaces them instead of mixing two
			// numberings (CORE-027, CORE-153). An empty set turning
			// non-empty needs no gap: nothing was shown.
			if gapFirst || sent > 0 {
				if err := writeSSEFrame(w, "id: %d-0\nevent: gap\ndata: {}\n\n", cur); err != nil {
					return false
				}
			}
			gapFirst, epoch, sent = false, cur, 0
		}
		for _, entry := range entries[sent:] {
			sent++
			b, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			if err := writeSSEFrame(w, "id: %d-%d\nevent: sublog\ndata: %s\n\n", epoch, sent, b); err != nil {
				return false
			}
		}
		flushSSE(w, flusher)
		return true
	}

	if !sendNew(initialEntries) {
		return
	}

	// G-01 (gaps_280426_2): 5-second cadence (was 1 second). FetchSubLogs
	// re-reads + re-parses every `.jsonl` line in the per-issue session
	// directory on every tick, scaling with `(open viewers × session size)`.
	// 5s is a stop-gap that 5x-reduces the disk/CPU cost while keeping
	// dashboard latency tolerable (most sublog activity is multi-second
	// agent reasoning, not sub-second). A proper fix tracks per-stream file
	// offsets and only reads appended bytes; deferred to a future T-NN.
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.streamsDone: // generation shut down (CORE-025)
			return
		case <-ticker.C:
			entries, err := s.client.FetchSubLogs(r.Context(), identifier)
			if err != nil {
				// T-45 (03.G-06): emit a structured SSE `error` frame before
				// returning so the dashboard can distinguish a tracker fetch
				// failure from a clean disconnect (user closed tab). Without
				// this, the per-issue sublog modal silently disappears with
				// no signal of what went wrong.
				writeSubLogErrorEvent(w, err)
				return
			}
			if !sendNew(entries) {
				return
			}
		}
	}
}

// subLogEpoch identifies a set of session-log entries for the sublog stream
// cursor (CORE-153): an FNV-1a hash of the FIRST entry's session id — the
// session file the set starts with — never 0 for a non-empty set (0 is the
// empty set). Appending keeps it; replacing or clearing the session files
// changes it. Keyed on the session id rather than the re-encoded entry
// (M6-close), so re-rendering the same first line (a parser change, a field
// filled in later) does not look like a replaced set. An entry without a
// session id falls back to its raw time and event.
func subLogEpoch(entries []domain.IssueLogEntry) uint32 {
	if len(entries) == 0 {
		return 0
	}
	first := entries[0]
	key := first.SessionID
	if key == "" {
		key = "\x00" + first.Time + "\x00" + first.Event
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return max(h.Sum32(), 1)
}

// writeSubLogErrorEvent emits an SSE `event: error` frame carrying a JSON
// payload `{code, message}` so the dashboard can render a toast or banner
// instead of treating the disconnect as benign. T-45 (03.G-06).
func writeSubLogErrorEvent(w http.ResponseWriter, err error) {
	const code = "fetch_failed"
	// JSON-encode inline to keep this self-contained — the payload is small
	// enough that pulling in encoding/json's error path is overkill.
	msg := err.Error()
	// Replace characters that would break the SSE single-line data: framing.
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	// Produce a compact JSON envelope; quoting via fmt.Sprintf is safe here
	// because both code and the cleaned message are plain ASCII strings —
	// %q escapes embedded quotes for us.
	payload := fmt.Sprintf(`{"code":%q,"message":%q}`, code, msg)
	if werr := writeSSEFrame(w, "event: error\ndata: %s\n\n", payload); werr == nil {
		if flusher, ok := w.(http.Flusher); ok {
			flushSSE(w, flusher)
		}
	}
}
