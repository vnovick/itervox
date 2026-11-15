package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/vnovick/itervox/internal/hub"
	"github.com/vnovick/itervox/internal/server"
)

// `itervox watch` (#120, #240): a read-only live panel over a running
// daemon's API. It shows the status header, one lane per running issue with
// its latest (redacted) activity, the events of the feed (#213) as they
// happen, and the input requests and guardrail approvals waiting for a
// human, each with the exact command that answers it.
//
// It never takes keystrokes and never changes a terminal mode: no raw mode,
// no alternate screen, no stdin. On a terminal the panel is redrawn in
// place with a clear-screen sequence; anywhere else (a pipe, a file, a CI
// log) it prints the status when it changes and each event once. So it
// works over SSH, in tmux and without a terminal, and it cannot leave a
// shell broken.

// watchFlags are the flags only watch reads.
type watchFlags struct {
	interval   time.Duration
	once       bool
	issue      string
	eventsOnly bool
	json       bool
	since      int64
}

func (f *watchFlags) register(fs *flag.FlagSet) {
	fs.DurationVar(&f.interval, "interval", 2*time.Second, "watch: refresh interval")
	fs.BoolVar(&f.once, "once", false, "watch: print the view once and exit")
	fs.StringVar(&f.issue, "issue", "", "watch: follow one issue: its full (redacted) log and its events")
	fs.BoolVar(&f.eventsOnly, "events-only", false, "watch: the event stream alone, one line per event (for piping)")
	fs.BoolVar(&f.json, "json", false, "watch: one JSON object per line")
	fs.Int64Var(&f.since, "since", -1, "watch: replay the feed events after this cursor (the seq itervox watch prints when it stops)")
}

// watchOptions is how one watch runs.
type watchOptions struct {
	interval   time.Duration
	once       bool
	issue      string
	eventsOnly bool
	json       bool
	// since is the feed cursor to start after; -1 starts at the feed's
	// head (no history).
	since int64
	// tty: out is a terminal, so the panel is redrawn in place. color: ANSI
	// colours (a terminal, and NO_COLOR unset).
	tty, color bool
	// target are the flags that reach the same daemon, for printed commands.
	target []string
	// hubLine is the hub status line of the header ("" when this daemon
	// does not serve the hub); nil when unknown.
	hubLine func() string
}

// options resolves f against the environment and out.
func (f *watchFlags) options(df daemonFlags, out io.Writer, getenv func(string) string) (watchOptions, error) {
	if f.interval <= 0 {
		return watchOptions{}, errors.New("--interval must be positive")
	}
	if f.since < -1 {
		return watchOptions{}, errors.New("--since must be a feed cursor (0 or more)")
	}
	tty := writerIsTerminal(out)
	return watchOptions{
		interval: f.interval, once: f.once, issue: f.issue, eventsOnly: f.eventsOnly, json: f.json,
		since: f.since, tty: tty, color: wantColor(getenv, tty) && !f.json, target: df.targetArgs(),
	}, nil
}

// writerIsTerminal reports whether out is a terminal. It only asks; it never
// changes the terminal's mode.
func writerIsTerminal(out io.Writer) bool {
	f, ok := out.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

// wantColor: colour on a terminal unless NO_COLOR is set (no-color.org: any
// non-empty value) or TERM is dumb.
func wantColor(getenv func(string) string, tty bool) bool {
	return tty && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
}

// localHubLine reports whether the daemon of workflow serves the machine's
// hub (#228), from the hub lock and the daemon's pid file: local files, no
// token.
// The answer is cached for hubLineTTL: the panel asks several times a tick.
func localHubLine(workflow string) func() string {
	var line string
	var at time.Time
	return func() string {
		if !at.IsZero() && time.Since(at) < hubLineTTL {
			return line
		}
		line, at = "", time.Now()
		dir, err := daemonRegistryDir()
		if err != nil {
			return line
		}
		info, ok, _ := hub.ReadLockInfo(dir)
		if !ok {
			return line
		}
		if pid, _, _, err := readPIDFile(workflow); err == nil && pid == info.PID {
			line = fmt.Sprintf("this daemon serves the hub at %s (%d projects)", info.URL, len(hubMembersIn(dir)))
		}
		return line
	}
}

// hubLineTTL is how long localHubLine's answer is reused.
const hubLineTTL = 30 * time.Second

// laneActivity caches a lane's activity line, refreshed when its key (the
// run's last event time and turn) changes.
type laneActivity struct {
	key, line string
}

// watcher is one running watch.
type watcher struct {
	c      *daemonClient
	opt    watchOptions
	out    io.Writer // lockedWriter: --issue streams logs from a second goroutine
	errOut io.Writer
	p      paint
	// cursor is the feed cursor: the seq of the last event read (-1 until
	// known). It survives a dropped connection, so a reconnect continues
	// where it left off.
	cursor   int64
	feedOff  bool
	activity map[string]laneActivity
	recent   []string // the panel's latest events, rendered
	lastKey  string
	lastErr  string // --events-only: the last connection problem reported
}

// lockedWriter serializes writes from the poll loop and the log follower.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// watchRecentEvents is how many events the terminal panel keeps on screen.
const watchRecentEvents = 12

// feedPagesPerTick bounds one tick's catch-up; the rest is read next tick.
const feedPagesPerTick = 20

// watchCommand runs watch until ctx ends (or once) and returns the exit
// code: 0, or 1 when --once could not read the daemon.
func watchCommand(ctx context.Context, c *daemonClient, opt watchOptions, out, errOut io.Writer) int {
	w := &watcher{c: c, opt: opt, out: &lockedWriter{w: out}, errOut: errOut, p: paint(opt.color),
		cursor: opt.since, activity: map[string]laneActivity{}}
	if opt.issue != "" && !opt.eventsOnly {
		return w.followIssue(ctx)
	}
	for {
		code, done := w.tick(ctx)
		if done {
			return code
		}
		select {
		case <-ctx.Done():
			w.stopped()
			return 0
		case <-time.After(opt.interval):
		}
	}
}

// stopped tells the operator how to resume the event stream.
func (w *watcher) stopped() {
	if w.cursor >= 0 && !w.feedOff {
		_, _ = fmt.Fprintf(w.errOut, "itervox watch: stopped at feed cursor %d; resume with: itervox watch --since %d\n", w.cursor, w.cursor)
	}
}

// tick reads the daemon once and prints what changed. done reports that
// watch ends (with code).
func (w *watcher) tick(ctx context.Context) (code int, done bool) {
	now := time.Now()
	var st *watchState
	var stErr error
	if !w.opt.eventsOnly {
		st, stErr = w.readState(ctx)
		if ctx.Err() != nil {
			w.stopped()
			return 0, true // stopped (Ctrl-C) while a request was in flight: not a failure
		}
		if stErr == nil {
			w.refreshActivity(ctx, st)
		}
	}
	events, gap, feedErr := w.readFeed(ctx)
	if ctx.Err() != nil {
		w.stopped()
		return 0, true
	}
	err := cmp2(stErr, feedErr)
	if err != nil {
		if w.opt.once {
			_, _ = fmt.Fprintln(w.errOut, "itervox watch: "+err.Error())
			return 1, true
		}
		w.c.rediscover() // a restarted daemon may listen on a new port
	}
	if w.opt.eventsOnly && !w.opt.once {
		// No status block to carry the problem: say it once on stderr.
		if msg := errString(feedErr); msg != w.lastErr {
			w.lastErr = msg
			if msg != "" {
				_, _ = fmt.Fprintln(w.errOut, "itervox watch: "+msg+" (retrying)")
			}
		}
	}
	switch {
	case w.opt.json:
		w.emitJSON(st, stErr, events, gap, now)
	case w.opt.tty && !w.opt.once && !w.opt.eventsOnly:
		w.drawPanel(st, stErr, events, gap, now)
	default:
		w.emitText(st, stErr, events, gap, now)
	}
	if w.opt.once {
		return 0, true
	}
	return 0, false
}

// errString is err's message, "" for nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// cmp2 is the first non-nil error.
func cmp2(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func (w *watcher) readState(ctx context.Context) (*watchState, error) {
	rctx, cancel := context.WithTimeout(ctx, clientTimeout)
	defer cancel()
	var s watchState
	if err := w.c.request(rctx, http.MethodGet, "/state", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// feedGap is a stretch of events that left the daemon's feed window before
// watch read them.
type feedGap struct {
	after, oldest int64
}

// readFeed reads the feed events after the cursor and advances it. The
// first read without --since only learns the head: watch shows what
// happens from now on.
func (w *watcher) readFeed(ctx context.Context) ([]server.FeedEvent, *feedGap, error) {
	if w.feedOff || (w.opt.once && w.cursor < 0) {
		return nil, nil, nil // --once without --since shows no events
	}
	var events []server.FeedEvent
	var gap *feedGap
	for range feedPagesPerTick {
		since, limit := w.cursor, 500
		if since < 0 {
			since, limit = 0, 1
		}
		var page server.FeedPage
		rctx, cancel := context.WithTimeout(ctx, clientTimeout)
		err := w.c.request(rctx, http.MethodGet, "/feed?since="+strconv.FormatInt(since, 10)+"&limit="+strconv.Itoa(limit), nil, &page)
		cancel()
		var ae *apiError
		if errors.As(err, &ae) && (ae.status == http.StatusServiceUnavailable || ae.status == http.StatusNotFound) {
			w.feedOff = true // an older daemon, or no feed: the panel still works
			_, _ = fmt.Fprintln(w.errOut, "itervox watch: this daemon serves no event feed; showing its state only")
			return events, gap, nil
		}
		if err != nil {
			return events, gap, err
		}
		if w.cursor < 0 {
			w.cursor = page.HeadSeq
			return nil, nil, nil
		}
		if page.Truncated && gap == nil && page.OldestSeq > w.cursor+1 {
			gap = &feedGap{after: w.cursor, oldest: page.OldestSeq}
		}
		events = append(events, page.Events...)
		w.cursor = page.NextSeq
		if !page.HasMore {
			break
		}
	}
	return events, gap, nil
}

// refreshActivity fetches the run context (#215) of each lane whose run
// moved since the last fetch; the daemon redacts it and never includes tool
// output. A daemon without run contexts keeps the state's last event line.
func (w *watcher) refreshActivity(ctx context.Context, st *watchState) {
	hasContext := false
	for _, c := range st.Capabilities {
		hasContext = hasContext || c == server.CapabilityRunContext
	}
	live := map[string]bool{}
	for _, r := range st.Running {
		live[r.Identifier] = true
		key := r.LastEventAt + "|" + strconv.Itoa(r.TurnCount)
		if a, ok := w.activity[r.Identifier]; ok && a.key == key {
			continue
		}
		line := r.LastEvent
		if hasContext {
			var rc server.RunContextResponse
			rctx, cancel := context.WithTimeout(ctx, clientTimeout)
			err := w.c.request(rctx, http.MethodGet, "/issues/"+url.PathEscape(r.Identifier)+"/context", nil, &rc)
			cancel()
			if err == nil && rc.Run != nil {
				line = cmpStr(activityLine(rc.Run), line)
			}
		}
		w.activity[r.Identifier] = laneActivity{key: key, line: redactLine(line)}
	}
	for id := range w.activity {
		if !live[id] {
			delete(w.activity, id)
		}
	}
}

// cmpStr is the first non-empty string.
func cmpStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// activityLine is a run's latest activity: its last tool call (name and
// target, never output) and its current step.
func activityLine(run *server.RunContextRow) string {
	tool := ""
	if t := run.LastToolCall; t != nil && t.Name != "" {
		tool = "[" + t.Name + "]"
		if t.Target != "" {
			tool += " " + t.Target
		}
	}
	switch {
	case tool != "" && run.CurrentStep != "":
		return tool + " — " + run.CurrentStep
	case tool != "":
		return tool
	}
	return run.CurrentStep
}
