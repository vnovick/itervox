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

	"github.com/vnovick/itervox/internal/logging"
)

// Terminal commands (#120, #240). They talk to a running daemon's API (or,
// with --project, to one project through the machine's hub), so they work
// over SSH and without a terminal. None of them reads stdin or changes a
// terminal mode.

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
	var wf watchFlags
	wf.register(fs)
	follow := fs.Bool("f", false, "logs: keep following new lines")
	reason := fs.String("reason", "", "pause (all dispatch): why, shown on the dashboard")
	pauseFor := fs.Duration("for", 0, "pause (all dispatch): resume by itself after this long, e.g. 2h")
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
		if len(pos) != 0 {
			return usage("usage: itervox watch [--issue ID] [--project NAME] [--events-only] [--json] [--since SEQ] [--once]")
		}
		opt, err := wf.options(df, out, os.Getenv)
		if err != nil {
			return usage(err.Error())
		}
		if opt.hubLine == nil && df.url == "" && df.project == "" {
			opt.hubLine = localHubLine(df.workflow)
		}
		return watchCommand(ctx, c, opt, out, errOut)
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
		if len(pos) == 0 { // #213: all dispatch
			return dispatchPauseCommand(rctx, c, name, *reason, *pauseFor, out, fail, usage)
		}
		if len(pos) != 1 || *reason != "" || *pauseFor != 0 {
			return usage("usage: itervox " + name + " <issue> | itervox pause [--reason r] [--for 2h] | itervox resume")
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
	case "approve": // #240: answer a guardrail approval request
		if len(pos) != 1 {
			return usage("usage: itervox approve <issue>")
		}
		if err := approveCommand(rctx, c, pos[0], out); err != nil {
			return fail(err)
		}
	case "reply":
		if len(pos) < 2 {
			return usage("usage: itervox reply <issue> <text>")
		}
		msg := strings.Join(pos[1:], " ")
		if err := c.request(rctx, http.MethodPost, issuePath()+"/provide-input", map[string]string{"message": msg}, nil); err != nil {
			return fail(err)
		}
		// 202 means queued, not applied (CORE-005): the reply reaches the
		// agent only if the issue is waiting for input.
		_, _ = fmt.Fprintf(out, "Reply to %s queued; its agent resumes with it if the issue is waiting for input (itervox watch shows those).\n", pos[0])
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

// logEntry is one line of GET /issues/{id}/logs and the log stream.
type logEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Tool    string `json:"tool"`
	Time    string `json:"time"`
}

func (e logEntry) String() string {
	msg := logging.RedactString(e.Message) // #240: every terminal log line is redacted
	if e.Tool != "" {
		msg = "[" + e.Tool + "] " + msg
	}
	return strings.TrimSpace(fmt.Sprintf("%s %-5s %s", e.Time, strings.ToUpper(e.Level), msg))
}

// printLogs prints identifier's run log; with follow it streams new lines
// (GET /issues/{id}/log-stream, reconnecting from the last line seen) until
// ctx ends.
func printLogs(ctx context.Context, c *daemonClient, identifier string, follow bool, out io.Writer) error {
	return followLogs(ctx, c, identifier, follow, func(e *logEntry) {
		if e == nil {
			_, _ = fmt.Fprintln(out, "… (earlier lines were dropped from the buffer)")
			return
		}
		_, _ = fmt.Fprintln(out, e.String())
	})
}

// followLogs is printLogs with each line (nil for a gap: lines dropped from
// the daemon's buffer) handed to emit.
func followLogs(ctx context.Context, c *daemonClient, identifier string, follow bool, emit func(*logEntry)) error {
	path := "/issues/" + url.PathEscape(identifier)
	if !follow {
		rctx, cancel := context.WithTimeout(ctx, clientTimeout)
		defer cancel()
		var entries []logEntry
		if err := c.request(rctx, http.MethodGet, path+"/logs", nil, &entries); err != nil {
			return err
		}
		for i := range entries {
			emit(&entries[i])
		}
		return nil
	}
	lastID := ""
	refused := 0
	for ctx.Err() == nil {
		err := streamLogs(ctx, c, path+"/log-stream", &lastID, emit)
		var ae *apiError
		if errors.As(err, &ae) {
			// Refused: a retry would be refused too, except right after a
			// restart, when the new token file may not have been read yet.
			if ae.status != http.StatusUnauthorized || c.discover == nil || refused >= 5 {
				return err
			}
			refused++
		} else {
			refused = 0
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second): // the daemon restarted or the connection dropped
		}
		c.rediscover() // a restarted daemon may listen on a new port, with a new token
	}
	return nil
}

// streamLogs reads one log-stream connection; lastID is the last event id,
// sent back as Last-Event-ID so a reconnect resumes after it.
func streamLogs(ctx context.Context, c *daemonClient, path string, lastID *string, emit func(*logEntry)) error {
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
					emit(&e)
				}
			case "gap":
				emit(nil)
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
