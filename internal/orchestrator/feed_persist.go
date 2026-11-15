package orchestrator

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// #213 — feed.jsonl, next to backend_health.json in the daemon log
// directory (runtime state, never committed). Written through the CORE-038
// ledger writer: marshalled on the event loop from the ring, dirty-checked,
// latest-wins, so a burst of events costs one write.
//
// Format: the first line is a header {"version":1,"lastSeq":N}; every other
// line is one FeedEvent, oldest first. The header keeps the last assigned
// seq even when every event has aged out, so seq never restarts at 1 and a
// client's cursor stays meaningful across restarts.

// feedFileVersion is the feed.jsonl header version.
const feedFileVersion = 1

type feedFileHeader struct {
	Version int   `json:"version"`
	LastSeq int64 `json:"lastSeq"`
}

// SetFeedFile sets where the event feed is persisted (#213). Must be called
// before Run; empty disables persistence.
func (o *Orchestrator) SetFeedFile(path string) {
	o.feedFile = path
}

// encodeFeed renders the ring as feed.jsonl.
func encodeFeed(feed []FeedEvent, lastSeq int64) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(64 + len(feed)*160)
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(feedFileHeader{Version: feedFileVersion, LastSeq: lastSeq}); err != nil {
		return nil, err
	}
	for _, ev := range feed {
		if err := enc.Encode(ev); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// saveFeedToDisk persists the ring. Event loop only.
func (o *Orchestrator) saveFeedToDisk(state *State) {
	if o.feedFile == "" || state == nil {
		return
	}
	data, err := encodeFeed(state.Feed, state.FeedSeq)
	if err != nil {
		slog.Warn("orchestrator: failed to marshal the event feed", "error", err)
		return
	}
	o.persistLedger(ledgerFeed, o.feedFile, data, 0o644)
}

// decodeFeed parses feed.jsonl. A malformed event line is skipped (and
// counted); a missing or malformed header is an error.
func decodeFeed(data []byte) (events []FeedEvent, lastSeq int64, skipped int, err error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	if !sc.Scan() {
		return nil, 0, 0, fmt.Errorf("orchestrator: feed.jsonl has no header")
	}
	var hdr feedFileHeader
	if err := json.Unmarshal(sc.Bytes(), &hdr); err != nil {
		return nil, 0, 0, fmt.Errorf("orchestrator: feed.jsonl header: %w", err)
	}
	if hdr.Version != feedFileVersion {
		return nil, 0, 0, fmt.Errorf("orchestrator: unsupported feed.jsonl version %d (want %d)", hdr.Version, feedFileVersion)
	}
	lastSeq = hdr.LastSeq
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev FeedEvent
		if json.Unmarshal(line, &ev) != nil || ev.Seq <= 0 || ev.Kind == "" ||
			(len(events) > 0 && ev.Seq <= events[len(events)-1].Seq) {
			skipped++
			continue
		}
		events = append(events, ev)
		lastSeq = max(lastSeq, ev.Seq)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, 0, fmt.Errorf("orchestrator: feed.jsonl: %w", err)
	}
	return events, lastSeq, skipped, nil
}

// loadFeedFromDisk restores the ring and the seq at startup, applying the
// FeedCap / FeedMaxAge bounds. A missing file is normal; an unreadable one
// is quarantined and the feed starts empty.
func (o *Orchestrator) loadFeedFromDisk(state State) State {
	if o.feedFile == "" {
		return state
	}
	data, err := os.ReadFile(o.feedFile)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load the event feed", "path", o.feedFile, "error", err)
		}
		return state
	}
	events, lastSeq, skipped, err := decodeFeed(data)
	if err != nil {
		o.quarantineLedgerFile(o.feedFile, data, err)
		return state
	}
	if skipped > 0 {
		slog.Warn("orchestrator: skipped malformed event feed lines", "path", o.feedFile, "skipped", skipped)
	}
	state.Feed, state.FeedSeq = events, lastSeq
	trimFeed(&state, time.Now())
	slog.Info("orchestrator: loaded event feed", "path", o.feedFile, "events", len(state.Feed), "last_seq", state.FeedSeq)
	return state
}
