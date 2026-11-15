package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/logging"
)

// `itervox watch --issue ID` (#240): one issue's lane, its full (redacted)
// log as it grows, and its feed events.

// followIssue is --issue: the issue's full log (redacted), followed until
// ctx ends, with its feed events and its input requests interleaved.
func (w *watcher) followIssue(ctx context.Context) int {
	if st, err := w.readState(ctx); err == nil {
		w.printIssueHeader(st)
	} else if w.opt.once {
		_, _ = fmt.Fprintln(w.errOut, "itervox watch: "+err.Error())
		return 1
	}
	emit := func(e *logEntry) { w.printLog(e) }
	if w.opt.once {
		if err := followLogs(ctx, w.c, w.opt.issue, false, emit); err != nil {
			_, _ = fmt.Fprintln(w.errOut, "itervox watch: "+err.Error())
			return 1
		}
		return 0
	}
	logsDone := make(chan error, 1)
	go func() {
		defer failFastOnPanic("watch-issue-logs")
		logsDone <- followLogs(ctx, w.c, w.opt.issue, true, emit)
	}()
	for {
		events, gap, err := w.readFeed(ctx)
		if err != nil && ctx.Err() == nil {
			w.c.rediscover()
		}
		w.printEvents(events, gap)
		select {
		case <-ctx.Done():
			<-logsDone
			w.stopped()
			return 0
		case err := <-logsDone:
			if err != nil {
				_, _ = fmt.Fprintln(w.errOut, "itervox watch: "+err.Error())
				return 1
			}
			return 0
		case <-time.After(w.opt.interval):
		}
	}
}

// printIssueHeader is --issue's opening: the issue's lane and, when it
// waits for a human, the command that answers it.
func (w *watcher) printIssueHeader(st *watchState) {
	if w.opt.json {
		w.jsonLine(w.statusJSON(st, time.Now()))
		return
	}
	var b strings.Builder
	running := false
	for _, r := range st.Running {
		running = running || r.Identifier == w.opt.issue
	}
	if running {
		b.WriteString(w.renderStatus(st, time.Now(), true))
	} else {
		fmt.Fprintf(&b, "%s is not running; its log so far:\n", w.opt.issue)
	}
	var ps []prompt
	for _, pr := range w.prompts(st) {
		if pr.Identifier == w.opt.issue {
			ps = append(ps, pr)
		}
	}
	w.renderPrompts(&b, ps)
	b.WriteString("\n")
	_, _ = io.WriteString(w.out, b.String())
}

// printLog prints one log line of --issue (nil: lines were dropped).
func (w *watcher) printLog(e *logEntry) {
	if e == nil {
		if w.opt.json {
			w.jsonLine(map[string]any{"type": "log_gap", "identifier": w.opt.issue})
			return
		}
		_, _ = fmt.Fprintln(w.out, w.p.c(sgrDim, "… (earlier lines were dropped from the buffer)"))
		return
	}
	if w.opt.json {
		w.jsonLine(struct {
			Type       string `json:"type"`
			Identifier string `json:"identifier"`
			logEntry
		}{"log", w.opt.issue, logEntry{Level: e.Level, Tool: e.Tool, Time: e.Time, Message: logging.RedactString(e.Message)}})
		return
	}
	clean := *e
	clean.Message = redactLine(e.Message) // no escape sequence from agent output reaches the terminal
	line := clean.String()
	switch strings.ToLower(e.Level) {
	case "error":
		line = w.p.c(sgrRed, line)
	case "warn", "warning":
		line = w.p.c(sgrYellow, line)
	}
	_, _ = fmt.Fprintln(w.out, line)
}
