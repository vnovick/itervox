package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
)

// Terminal commands that replace the TUI (#120). They talk to a running
// daemon's API, so they work over SSH and without a terminal.

func runClientCommand(name string, args []string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fatalExit(clientCommand(ctx, name, args, os.Stdout, os.Stderr))
}

// clientCommand runs one command and returns its exit code: 0 done, 1 the
// daemon refused or could not be reached, 2 usage.
func clientCommand(ctx context.Context, name string, args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var df daemonFlags
	df.register(fs)
	interval := fs.Duration("interval", 2*time.Second, "watch: refresh interval")
	once := fs.Bool("once", false, "watch: print the view once and exit")
	follow := fs.Bool("f", false, "logs: keep following new lines")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	usage := func(msg string) int {
		_, _ = fmt.Fprintln(errOut, "itervox "+name+": "+msg)
		return 2
	}
	c, err := df.client()
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "itervox "+name+": "+err.Error())
		return 1
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintln(errOut, "itervox "+name+": "+err.Error())
		return 1
	}
	switch name {
	case "watch":
		return watchDaemon(ctx, c, *interval, *once, out, errOut)
	case "logs":
		if len(pos) != 1 {
			return usage("usage: itervox logs [-f] <issue>")
		}
		if err := printLogs(ctx, c, pos[0], *follow, out); err != nil {
			return fail(err)
		}
		return 0
	}
	rctx, cancel := context.WithTimeout(ctx, clientTimeout)
	defer cancel()
	issuePath := func() string { return "/issues/" + url.PathEscape(pos[0]) }
	switch name {
	case "pause", "resume":
		if len(pos) != 1 {
			return usage("usage: itervox " + name + " <issue>")
		}
		endpoint, done := "/cancel", "Paused %s; its agent was stopped.\n"
		if name == "resume" {
			endpoint, done = "/resume", "Resumed %s.\n"
		}
		if err := c.request(rctx, http.MethodPost, issuePath()+endpoint, nil, nil); err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintf(out, done, pos[0])
	case "dispatch":
		if len(pos) != 1 {
			return usage("usage: itervox dispatch <issue>")
		}
		var st struct {
			ActiveStates []string `json:"activeStates"`
		}
		if err := c.request(rctx, http.MethodGet, "/state", nil, &st); err != nil {
			return fail(err)
		}
		if len(st.ActiveStates) == 0 {
			return fail(errors.New("the daemon reports no active states"))
		}
		body := map[string]string{"state": st.ActiveStates[0]}
		if err := c.request(rctx, http.MethodPatch, issuePath()+"/state", body, nil); err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintf(out, "Moved %s to %s; it is dispatched when a slot is free.\n", pos[0], st.ActiveStates[0])
	case "workers":
		n := 0
		if len(pos) == 1 {
			n, _ = strconv.Atoi(pos[0])
		}
		if n < 1 {
			return usage("usage: itervox workers <n> (n ≥ 1)")
		}
		var res struct {
			Workers int `json:"workers"`
		}
		if err := c.request(rctx, http.MethodPost, "/settings/workers", map[string]int{"workers": n}, &res); err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintf(out, "Workers: %d.\n", res.Workers)
	case "reply":
		if len(pos) < 2 {
			return usage("usage: itervox reply <issue> <text>")
		}
		msg := strings.Join(pos[1:], " ")
		if err := c.request(rctx, http.MethodPost, issuePath()+"/provide-input", map[string]string{"message": msg}, nil); err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintf(out, "Replied to %s; its agent resumes with your answer.\n", pos[0])
	default:
		return usage("unknown command")
	}
	return 0
}

// parseInterspersed parses fs from args, allowing flags after positional
// arguments (`itervox logs ENG-1 -f`).
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// watchState is the part of GET /api/v1/state that watch shows.
type watchState struct {
	MaxConcurrentAgents int `json:"maxConcurrentAgents"`
	Running             []struct {
		Identifier string `json:"identifier"`
		State      string `json:"state"`
		Kind       string `json:"kind"`
		Backend    string `json:"backend"`
		WorkerHost string `json:"workerHost"`
		TurnCount  int    `json:"turnCount"`
		Tokens     int    `json:"tokens"`
		ElapsedMs  int64  `json:"elapsedMs"`
		LastEvent  string `json:"lastEvent"`
	} `json:"running"`
	Retrying []struct {
		Identifier string    `json:"identifier"`
		Attempt    int       `json:"attempt"`
		DueAt      time.Time `json:"dueAt"`
		Error      string    `json:"error"`
	} `json:"retrying"`
	InputRequired []struct {
		Identifier string `json:"identifier"`
		State      string `json:"state"`
		Context    string `json:"context"`
	} `json:"inputRequired"`
	AutomationQueue []struct {
		Identifier   string `json:"identifier"`
		AutomationID string `json:"automationId"`
		Reason       string `json:"reason"`
	} `json:"automationQueue"`
	Paused       []string          `json:"paused"`
	PauseReasons map[string]string `json:"pauseReasons"`
}

// renderWatch is the watch view of s at now, in plain text.
func renderWatch(s watchState, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "itervox · %d/%d agents · %d retrying · %d waiting for input · %d queued · %d paused\n",
		len(s.Running), s.MaxConcurrentAgents, len(s.Retrying), len(s.InputRequired), len(s.AutomationQueue), len(s.Paused))
	section := func(title string, n int) bool {
		if n == 0 {
			return false
		}
		fmt.Fprintf(&b, "\n%s\n", title)
		return true
	}
	if section("RUNNING", len(s.Running)) {
		for _, r := range s.Running {
			who := r.Backend
			if r.Kind != "" && r.Kind != "worker" {
				who += " " + r.Kind
			}
			if r.WorkerHost != "" {
				who += " @" + r.WorkerHost
			}
			fmt.Fprintf(&b, "  %-10s %-14s %-18s %6s  turn %-3d %7s tok  %s\n", r.Identifier, clip(r.State, 14), clip(who, 18),
				shortDuration(time.Duration(r.ElapsedMs)*time.Millisecond), r.TurnCount, fmtTokens(r.Tokens), clip(oneLine(r.LastEvent), 60))
		}
	}
	if section("RETRYING", len(s.Retrying)) {
		for _, r := range s.Retrying {
			in := "now"
			if d := r.DueAt.Sub(now); d > 0 {
				in = "in " + shortDuration(d)
			}
			fmt.Fprintf(&b, "  %-10s attempt %-3d %-9s %s\n", r.Identifier, r.Attempt, in, clip(oneLine(r.Error), 70))
		}
	}
	if section("WAITING FOR INPUT", len(s.InputRequired)) {
		for _, r := range s.InputRequired {
			fmt.Fprintf(&b, "  %-10s %s\n", r.Identifier, clip(oneLine(r.Context), 90))
		}
	}
	if section("QUEUED", len(s.AutomationQueue)) {
		for _, q := range s.AutomationQueue {
			fmt.Fprintf(&b, "  %-10s %-24s %s\n", q.Identifier, clip(q.AutomationID, 24), strings.ReplaceAll(q.Reason, "_", " "))
		}
	}
	if section("PAUSED", len(s.Paused)) {
		for _, id := range s.Paused {
			if reason := s.PauseReasons[id]; reason != "" {
				fmt.Fprintf(&b, "  %-10s %s\n", id, strings.ReplaceAll(reason, "_", " "))
			} else {
				fmt.Fprintf(&b, "  %s\n", id)
			}
		}
	}
	if len(s.Running)+len(s.Retrying)+len(s.InputRequired)+len(s.AutomationQueue)+len(s.Paused) == 0 {
		b.WriteString("\nNothing running or waiting.\n")
	}
	return b.String()
}

// watchDaemon shows renderWatch until ctx ends: redrawn in place on a terminal,
// otherwise printed again only when it changes, with a time stamp.
func watchDaemon(ctx context.Context, c *daemonClient, every time.Duration, once bool, out, errOut io.Writer) int {
	tty := false
	if f, ok := out.(*os.File); ok {
		tty = term.IsTerminal(f.Fd())
	}
	last := ""
	for {
		rctx, cancel := context.WithTimeout(ctx, clientTimeout)
		var s watchState
		err := c.request(rctx, http.MethodGet, "/state", nil, &s)
		cancel()
		now := time.Now()
		view := ""
		if err != nil {
			if once {
				_, _ = fmt.Fprintln(errOut, "itervox watch: "+err.Error())
				return 1
			}
			view = "itervox watch: " + err.Error() + " (retrying)\n"
		} else {
			view = renderWatch(s, now)
		}
		switch {
		case once:
			_, _ = io.WriteString(out, view)
			return 0
		case tty:
			_, _ = io.WriteString(out, "\x1b[H\x1b[2J"+now.Format("15:04:05")+"  (Ctrl-C to quit)\n"+view)
		case view != last:
			_, _ = fmt.Fprintf(out, "--- %s ---\n%s", now.Format(time.RFC3339), view)
		}
		last = view
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(every):
		}
	}
}

// logEntry is one line of GET /issues/{id}/logs and the log stream.
type logEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Tool    string `json:"tool"`
	Time    string `json:"time"`
}

func (e logEntry) String() string {
	msg := e.Message
	if e.Tool != "" {
		msg = "[" + e.Tool + "] " + msg
	}
	return strings.TrimSpace(fmt.Sprintf("%s %-5s %s", e.Time, strings.ToUpper(e.Level), msg))
}

// printLogs prints identifier's run log; with follow it streams new lines
// (GET /issues/{id}/log-stream, reconnecting from the last line seen) until
// ctx ends.
func printLogs(ctx context.Context, c *daemonClient, identifier string, follow bool, out io.Writer) error {
	path := "/issues/" + url.PathEscape(identifier)
	if !follow {
		rctx, cancel := context.WithTimeout(ctx, clientTimeout)
		defer cancel()
		var entries []logEntry
		if err := c.request(rctx, http.MethodGet, path+"/logs", nil, &entries); err != nil {
			return err
		}
		for _, e := range entries {
			_, _ = fmt.Fprintln(out, e.String())
		}
		return nil
	}
	lastID := ""
	for ctx.Err() == nil {
		err := streamLogs(ctx, c, path+"/log-stream", &lastID, out)
		var ae *apiError
		if errors.As(err, &ae) {
			return err // refused: a retry would be refused too
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second): // the daemon restarted or the connection dropped
		}
	}
	return nil
}

// streamLogs reads one log-stream connection; lastID is the last event id,
// sent back as Last-Event-ID so a reconnect resumes after it.
func streamLogs(ctx context.Context, c *daemonClient, path string, lastID *string, out io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if *lastID != "" {
		req.Header.Set("Last-Event-ID", *lastID)
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return readAPIError(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var id, event, data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if id != "" {
				*lastID = id
			}
			switch event {
			case "log":
				var e logEntry
				if json.Unmarshal([]byte(data), &e) == nil {
					_, _ = fmt.Fprintln(out, e.String())
				}
			case "gap":
				_, _ = fmt.Fprintln(out, "… (earlier lines were dropped from the buffer)")
			}
			id, event, data = "", "", ""
		case strings.HasPrefix(line, "id:"):
			id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data += strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	return sc.Err()
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return strconv.Itoa(n)
	}
}
