package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/server"
)

// `itervox watch --json` (#240): one JSON object per line — a status line
// when the status changes, one line per feed event, a gap line, and with
// --issue one line per log line. Never coloured.

// jsonLine writes v as one JSON line.
func (w *watcher) jsonLine(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = w.out.Write(append(raw, '\n'))
}

// watchEventJSON is a feed event line of --json.
type watchEventJSON struct {
	Type string `json:"type"` // "event"
	server.FeedEvent
	Text    string `json:"text"`
	Command string `json:"command,omitempty"`
}

// watchStatusJSON is a status line of --json.
type watchStatusJSON struct {
	Type                  string          `json:"type"` // "status"
	At                    time.Time       `json:"at"`
	Running               int             `json:"running"`
	Slots                 int             `json:"slots"`
	Queued                int             `json:"queued"`
	Retrying              int             `json:"retrying"`
	WaitingForSlot        int             `json:"waitingForSlot"`
	BlockedByDependencies int             `json:"blockedByDependencies"`
	Paused                int             `json:"paused"`
	DispatchPaused        bool            `json:"dispatchPaused"`
	PauseReason           string          `json:"pauseReason,omitempty"`
	Hub                   string          `json:"hub,omitempty"`
	FeedCursor            int64           `json:"feedCursor"`
	Lanes                 []watchLaneJSON `json:"lanes"`
	NeedsYou              []prompt        `json:"needsYou"`
}

// watchLaneJSON is one lane of a --json status line.
type watchLaneJSON struct {
	Identifier       string   `json:"identifier"`
	Title            string   `json:"title,omitempty"`
	State            string   `json:"state"`
	Profile          string   `json:"profile,omitempty"`
	Backend          string   `json:"backend,omitempty"`
	Kind             string   `json:"kind,omitempty"`
	WorkerHost       string   `json:"workerHost,omitempty"`
	ElapsedMs        int64    `json:"elapsedMs"`
	Turn             int      `json:"turn"`
	Tokens           int      `json:"tokens"`
	CostUSDEstimated *float64 `json:"costUsdEstimated,omitempty"`
	Activity         string   `json:"activity,omitempty"`
}

func (w *watcher) statusJSON(s *watchState, now time.Time) watchStatusJSON {
	j := watchStatusJSON{Type: "status", At: now.UTC(), Running: len(s.Running), Slots: s.MaxConcurrentAgents,
		Queued: len(s.AutomationQueue), Retrying: len(s.Retrying), WaitingForSlot: s.DispatchPressure.EligibleWaiting,
		BlockedByDependencies: s.DispatchPressure.BlockedByDependency, Paused: len(s.Paused),
		FeedCursor: w.cursor, Lanes: []watchLaneJSON{}, NeedsYou: w.prompts(s)}
	if j.NeedsYou == nil {
		j.NeedsYou = []prompt{}
	}
	if dp := s.DispatchPaused; dp != nil {
		j.DispatchPaused, j.PauseReason = true, redactLine(dp.Reason)
	}
	if w.opt.hubLine != nil {
		j.Hub = w.opt.hubLine()
	}
	for _, r := range s.Running {
		if w.opt.issue != "" && r.Identifier != w.opt.issue {
			continue
		}
		j.Lanes = append(j.Lanes, watchLaneJSON{Identifier: r.Identifier, Title: redactLine(r.Title), State: r.State,
			Profile: r.Profile, Backend: r.Backend, Kind: r.Kind, WorkerHost: r.WorkerHost, ElapsedMs: r.ElapsedMs,
			Turn: r.TurnCount, Tokens: r.Tokens, CostUSDEstimated: r.CostUSDEstimated, Activity: w.activity[r.Identifier].line})
	}
	return j
}

// emitJSON is --json: a status line when the status changed (not counting
// the ticking fields), then one line per event.
func (w *watcher) emitJSON(st *watchState, stErr error, events []server.FeedEvent, gap *feedGap, now time.Time) {
	if st != nil {
		if key := w.renderStatus(st, now, false); key != w.lastKey {
			w.lastKey = key
			w.jsonLine(w.statusJSON(st, now))
		}
	} else if stErr != nil {
		_, _ = fmt.Fprintln(w.errOut, "itervox watch: "+stErr.Error()+" (retrying)")
	}
	w.printEvents(events, gap)
}

// stripSGR drops the colour codes paint added (the JSON text is plain).
func stripSGR(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return b.String() + s
		}
		b.WriteString(s[:i])
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			return b.String()
		}
		s = s[i+j+1:]
	}
}
