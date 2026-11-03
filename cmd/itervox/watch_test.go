package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/server"
)

// #240 — itervox watch as a read-only live panel.

// fakeFeed is a daemon feed with the cursor rules of GET /api/v1/feed
// (orchestrator.readFeed): events after since, at most pageSize per page,
// truncated when the cursor is older than the window.
type fakeFeed struct {
	mu       sync.Mutex
	events   []server.FeedEvent
	head     int64
	pageSize int
	sinces   []int64 // the since of every read, in order
}

func (f *fakeFeed) add(kind, identifier string, payload server.FeedPayload) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.head++
	f.events = append(f.events, server.FeedEvent{Seq: f.head, TS: time.Date(2026, 1, 2, 10, 0, int(f.head), 0, time.UTC),
		Kind: kind, Identifier: identifier, Payload: payload})
}

// dropBefore removes the events before seq from the window.
func (f *fakeFeed) dropBefore(seq int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.events) > 0 && f.events[0].Seq < seq {
		f.events = f.events[1:]
	}
}

func (f *fakeFeed) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	since, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	require.NoError(t, err)
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	require.NoError(t, err)
	if f.pageSize > 0 {
		limit = min(limit, f.pageSize)
	}
	f.sinces = append(f.sinces, since)
	page := server.FeedPage{Events: []server.FeedEvent{}, HeadSeq: f.head, NextSeq: min(since, f.head)}
	if len(f.events) > 0 {
		page.OldestSeq = f.events[0].Seq
		page.Truncated = since < page.OldestSeq-1
	}
	for i, ev := range f.events {
		if ev.Seq <= since {
			continue
		}
		if len(page.Events) == limit {
			page.HasMore = i < len(f.events)
			break
		}
		page.Events = append(page.Events, ev)
		page.NextSeq = ev.Seq
	}
	writeJSONBody(w, page)
}

func (f *fakeFeed) readSinces() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.sinces...)
}

// countPath counts the recorded calls to path.
func countPath(d *fakeDaemon, path string) int {
	n := 0
	for _, c := range d.recorded() {
		if c.path == path {
			n++
		}
	}
	return n
}

// TestWatchShowsLanePerRunningIssueWithLatestActivity: one lane per running
// issue with its identifier, title, profile and backend, elapsed time and
// turn, tokens and cost, and its latest activity: the run context's last
// tool call and step when the daemon serves run contexts (#215), else the
// state's last event line — redacted either way. The run context is read
// again only when the run moved.
func TestWatchShowsLanePerRunningIssueWithLatestActivity(t *testing.T) {
	const secret = "watch-fixture-registered-value" // an exact-value secret, not a token shape
	logging.RegisterSecret(secret)
	cost := 0.42
	state := map[string]any{
		"maxConcurrentAgents": 2,
		"capabilities":        []string{server.CapabilityRunContext},
		"running": []any{
			map[string]any{"identifier": "ENG-1", "title": "Fix the login race", "state": "In Progress", "profile": "coder",
				"backend": "claude", "turnCount": 3, "tokens": 12000, "costUsdEstimated": cost, "elapsedMs": 65000,
				"lastEvent": "an older line", "lastEventAt": "2026-01-02T10:00:00Z"},
			map[string]any{"identifier": "ENG-2", "title": "Add a cache", "state": "In Progress", "backend": "codex",
				"kind": "reviewer", "turnCount": 1, "tokens": 900, "elapsedMs": 5000,
				"lastEvent": "exporting " + secret + " to the build", "lastEventAt": "2026-01-02T10:00:01Z"},
		},
		"inputRequired": []any{map[string]any{"identifier": "ENG-9", "state": "input_required", "context": "Which region?"}},
	}
	d := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/state":
			writeJSONBody(w, state)
		case "/api/v1/issues/ENG-1/context":
			writeJSONBody(w, server.RunContextResponse{Identifier: "ENG-1", Run: &server.RunContextRow{Live: true,
				CurrentStep: "Running the test suite", LastToolCall: &server.RunToolCall{Name: "Bash", Target: "go test ./..."}}})
		default:
			w.WriteHeader(http.StatusNotFound) // ENG-2 has no run context: the state's line stands in
		}
	})
	code, out, errOut := runClient(t, t.Context(), d, "watch", "--once")
	require.Equal(t, 0, code, errOut)

	lanes := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "● ") {
			lanes++
		}
	}
	assert.Equal(t, 2, lanes, "one lane per running issue, none for an issue that only waits for input:\n%s", out)
	for _, want := range []string{
		"● ENG-1      Fix the login race", "coder · claude", "1m05s", "turn 3", "12.0k tok", "$0.42",
		"› [Bash] go test ./... — Running the test suite",
		"● ENG-2      Add a cache", "codex reviewer", "turn 1", "900 tok",
		"› exporting *** to the build",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, secret, "the activity line is redacted")
	assert.NotContains(t, out, "an older line", "the run context wins over the state's line")

	// Live: the context is read once per run move, not on every refresh.
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int)
	before := countPath(d, "/api/v1/issues/ENG-1/context")
	go func() {
		done <- clientCommand(ctx, "watch", []string{"--url", d.srv.URL, "--interval", "10ms"}, io.Discard, io.Discard)
	}()
	require.Eventually(t, func() bool { return countPath(d, "/api/v1/state") >= 6 }, 5*time.Second, 5*time.Millisecond)
	cancel()
	require.Equal(t, 0, <-done)
	assert.Equal(t, 1, countPath(d, "/api/v1/issues/ENG-1/context")-before, "an unchanged run is not read again")
}

// TestWatchHighlightsInputAndApprovalWithReplyCommand: an input request and
// a guardrail approval stand out under NEEDS YOU (in bold yellow on a
// colour terminal) with the exact command that answers each — reaching the
// same daemon, the identifier quoted for the shell — and the printed
// approve command works; watch itself never answers.
func TestWatchHighlightsInputAndApprovalWithReplyCommand(t *testing.T) {
	state := map[string]any{
		"maxConcurrentAgents": 2,
		"inputRequired": []any{
			map[string]any{"identifier": "ENG-3", "state": "input_required", "context": "Which database should I use?\nPostgres or SQLite?"},
			map[string]any{"identifier": "ENG-4", "state": "input_required", "guardrailApproval": true,
				"context": "Agent wants to run: `vercel --prod`\n\nThis command is in guardrails.approval_commands."},
			map[string]any{"identifier": "ENG-5", "state": "pending_input_resume", "context": "Reply received, waiting to resume."},
			map[string]any{"identifier": "#12", "state": "input_required", "context": "Ship it?"},
		},
	}
	feed := &fakeFeed{}
	feed.add("input_required", "ENG-3", server.FeedPayload{Profile: "coder", Backend: "claude"})
	feed.add("input_required", "ENG-4", server.FeedPayload{Kind: "guardrail_approval"})
	d := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/state":
			writeJSONBody(w, state)
		case "/api/v1/feed":
			feed.serve(t, w, r)
		default:
			writeJSONBody(w, map[string]any{"ok": true})
		}
	})
	code, out, errOut := runClient(t, t.Context(), d, "watch", "--once", "--since", "0")
	require.Equal(t, 0, code, errOut)
	url := d.srv.URL
	for _, want := range []string{
		"NEEDS YOU",
		"? ENG-3      question: Which database should I use? Postgres or SQLite?",
		"→ itervox reply --url " + url + ` ENG-3 "<your answer>"`,
		"! ENG-4      approval: Agent wants to run: `vercel --prod`",
		"→ itervox approve --url " + url + " ENG-4",
		"to refuse it: itervox reply --url " + url + ` ENG-4 "<why not>"`,
		"→ itervox reply --url " + url + ` '#12' "<your answer>"`,
		// the feed events carry the same commands
		"ENG-3      is waiting for your input (coder · claude)  → itervox reply --url " + url + ` ENG-3 "<your answer>"`,
		"ENG-4      wants approval to run a held command  → itervox approve --url " + url + " ENG-4",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "ENG-5", "an answered question is not highlighted")
	assert.Contains(t, out, "· 3 waiting for you ·", "the header counts the waiting requests")
	for _, c := range d.recorded() {
		assert.Equal(t, http.MethodGet, c.method, "watch only reads: %s %s", c.method, c.path)
	}

	// On a colour terminal the block is bold yellow.
	var st watchState
	raw, _ := json.Marshal(state)
	require.NoError(t, json.Unmarshal(raw, &st))
	colour := (&watcher{p: paint(true), activity: map[string]laneActivity{}}).renderStatus(&st, time.Now(), true)
	assert.Contains(t, colour, "\x1b[1;33mNEEDS YOU\x1b[0m")

	// The printed approve command answers the approval with "approve"…
	code, approveOut, errOut := runClient(t, t.Context(), d, "approve", "ENG-4")
	require.Equal(t, 0, code, errOut)
	assert.Equal(t, "Approved for ENG-4: the held command runs once.\n", approveOut)
	last := d.recorded()[len(d.recorded())-1]
	assert.Equal(t, "/api/v1/issues/ENG-4/provide-input", last.path)
	assert.JSONEq(t, `{"message":"approve"}`, last.body)
	// …and refuses a question, which must be answered with reply.
	n := len(d.recorded())
	code, _, errOut = runClient(t, t.Context(), d, "approve", "ENG-3")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "waiting for an answer, not an approval")
	for _, c := range d.recorded()[n:] {
		assert.NotEqual(t, http.MethodPost, c.method, "nothing is sent for a question")
	}
}

// TestWatchResumesEventsFromFeedCursor: events come from the feed by
// cursor. --since replays after a cursor (paging through the feed); live,
// watch starts at the head, prints each event once, continues after a
// dropped connection where it left off, reports events that left the
// window, and says how to resume when it stops.
func TestWatchResumesEventsFromFeedCursor(t *testing.T) {
	feed := &fakeFeed{pageSize: 2}
	for i := 1; i <= 6; i++ {
		feed.add("run_finished", "ENG-"+strconv.Itoa(i), server.FeedPayload{Outcome: "succeeded"})
	}
	var mu sync.Mutex
	down := false
	d := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		isDown := down
		mu.Unlock()
		if isDown {
			hj, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, _, err := hj.Hijack()
			require.NoError(t, err)
			_ = conn.Close() // the connection drops
			return
		}
		require.Equal(t, "/api/v1/feed", r.URL.Path, "--events-only reads only the feed")
		feed.serve(t, w, r)
	})

	// --since: a replay after the cursor, across pages.
	code, out, errOut := runClient(t, t.Context(), d, "watch", "--events-only", "--once", "--since", "2")
	require.Equal(t, 0, code, errOut)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 4, out)
	for i, seq := range []int{3, 4, 5, 6} {
		assert.Contains(t, lines[i], "#"+strconv.Itoa(seq)+" ENG-"+strconv.Itoa(seq)+"      finished: succeeded")
	}
	assert.Equal(t, []int64{2, 4}, feed.readSinces()[:2], "page after page from the cursor")

	// Live: from the head, each new event once, across a dropped connection.
	feed.sinces = nil
	ctx, cancel := context.WithCancel(t.Context())
	out2, errOut2 := &syncBuffer{}, &syncBuffer{}
	done := make(chan int)
	go func() {
		done <- clientCommand(ctx, "watch", []string{"--url", d.srv.URL, "--events-only", "--interval", "10ms"}, out2, errOut2)
	}()
	require.Eventually(t, func() bool { return len(feed.readSinces()) >= 3 }, 5*time.Second, 5*time.Millisecond)
	assert.Empty(t, out2.String(), "no history without --since")
	feed.add("pr_opened", "ENG-7", server.FeedPayload{PRURL: "https://example.com/pr/7"})
	feed.add("run_failed", "ENG-8", server.FeedPayload{Outcome: "failed"})
	require.Eventually(t, func() bool { return strings.Contains(out2.String(), "ENG-8") }, 5*time.Second, 5*time.Millisecond)
	mu.Lock()
	down = true
	mu.Unlock()
	feed.add("pr_merged", "ENG-9", server.FeedPayload{PRURL: "https://example.com/pr/9"})
	n := len(d.recorded())
	require.Eventually(t, func() bool { return len(d.recorded()) >= n+3 }, 5*time.Second, 5*time.Millisecond, "watch keeps retrying")
	upAt := len(feed.readSinces())
	mu.Lock()
	down = false
	mu.Unlock()
	require.Eventually(t, func() bool { return strings.Contains(out2.String(), "ENG-9") }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, int64(8), feed.readSinces()[upAt], "the reconnect continues after the last event read")

	// Events that left the window before they were read are reported.
	feed.add("run_started", "ENG-10", server.FeedPayload{})
	feed.add("run_started", "ENG-11", server.FeedPayload{})
	feed.add("run_started", "ENG-12", server.FeedPayload{})
	cancel()
	require.Equal(t, 0, <-done)
	got := out2.String()
	for _, id := range []string{"ENG-7", "ENG-8", "ENG-9"} {
		assert.Equal(t, 1, strings.Count(got, id+" "), "%s printed exactly once:\n%s", id, got)
	}
	assert.Contains(t, got, "#7 ENG-7      opened a pull request https://example.com/pr/7")
	assert.Contains(t, got, "#8 ENG-8      failed: failed")
	assert.Contains(t, got, "#9 ENG-9      pull request merged https://example.com/pr/9")
	assert.NotContains(t, got, "ENG-6 ", "history before the head is not replayed")
	cursor := strings.TrimSpace(errOut2.String()[strings.LastIndex(errOut2.String(), "itervox watch: stopped"):])
	assert.Regexp(t, `^itervox watch: stopped at feed cursor (9|1[0-2]); resume with: itervox watch --since (9|1[0-2])$`, cursor)

	feed.dropBefore(11)
	code, out, errOut = runClient(t, t.Context(), d, "watch", "--events-only", "--once", "--since", "9")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "… events 10–10 left the daemon's feed before they were read; continuing from 11")
	assert.Contains(t, out, "#11 ENG-11")
	assert.Contains(t, out, "#12 ENG-12")
}

// TestWatchJSONLinesAndNoColor: --json prints one JSON object per line — a
// status line when the status changes (not on every tick of the elapsed
// time) and one line per event, with the answering command — and never a
// colour code; colour needs a terminal and is off with NO_COLOR or
// TERM=dumb.
func TestWatchJSONLinesAndNoColor(t *testing.T) {
	cost := 1.5
	var mu sync.Mutex
	polls := 0
	feed := &fakeFeed{}
	feed.add("input_required", "ENG-2", server.FeedPayload{Kind: "guardrail_approval"})
	feed.add("run_finished", "ENG-1", server.FeedPayload{Outcome: "succeeded", Profile: "coder", Backend: "claude"})
	d := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/state":
			mu.Lock()
			polls++
			n := polls
			mu.Unlock()
			writeJSONBody(w, map[string]any{
				"maxConcurrentAgents": 2,
				"running": []any{map[string]any{"identifier": "ENG-1", "title": "Fix it", "state": "In Progress", "backend": "claude",
					"profile": "coder", "turnCount": 2, "tokens": 1000 + n, "elapsedMs": n * 1000, "costUsdEstimated": cost}},
				"inputRequired": []any{map[string]any{"identifier": "ENG-2", "state": "input_required", "guardrailApproval": true,
					"context": "Agent wants to run: `kubectl apply`"}},
			})
		case "/api/v1/feed":
			feed.serve(t, w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	code, out, errOut := runClient(t, t.Context(), d, "watch", "--json", "--once", "--since", "0")
	require.Equal(t, 0, code, errOut)
	assert.NotContains(t, out, "\x1b", "no colour in JSON")
	var objs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var obj map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &obj), "each line is one JSON object: %q", line)
		objs = append(objs, obj)
	}
	require.Len(t, objs, 3, out)
	assert.Equal(t, "status", objs[0]["type"])
	assert.EqualValues(t, 1, objs[0]["running"])
	lanes := objs[0]["lanes"].([]any)
	require.Len(t, lanes, 1)
	lane := lanes[0].(map[string]any)
	assert.Equal(t, "ENG-1", lane["identifier"])
	assert.Equal(t, "Fix it", lane["title"])
	assert.Equal(t, "coder", lane["profile"])
	assert.EqualValues(t, 1.5, lane["costUsdEstimated"])
	needs := objs[0]["needsYou"].([]any)
	require.Len(t, needs, 1)
	assert.Equal(t, "approval", needs[0].(map[string]any)["kind"])
	assert.Equal(t, "itervox approve --url "+d.srv.URL+" ENG-2", needs[0].(map[string]any)["command"])
	assert.Equal(t, "event", objs[1]["type"])
	assert.EqualValues(t, 1, objs[1]["seq"])
	assert.Equal(t, "input_required", objs[1]["kind"])
	assert.Equal(t, "itervox approve --url "+d.srv.URL+" ENG-2", objs[1]["command"])
	assert.Equal(t, "event", objs[2]["type"])
	assert.Equal(t, "run_finished", objs[2]["kind"])
	assert.Contains(t, objs[2]["text"], "ENG-1      finished: succeeded (coder · claude)")

	// Live: the ticking elapsed time and tokens do not repeat the status.
	ctx, cancel := context.WithCancel(t.Context())
	live := &syncBuffer{}
	done := make(chan int)
	go func() {
		done <- clientCommand(ctx, "watch", []string{"--url", d.srv.URL, "--json", "--interval", "10ms"}, live, io.Discard)
	}()
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return polls >= 6 }, 5*time.Second, 5*time.Millisecond)
	cancel()
	require.Equal(t, 0, <-done)
	assert.Equal(t, 1, strings.Count(live.String(), `"type":"status"`), live.String())

	// Colour: a terminal without NO_COLOR; never with NO_COLOR, TERM=dumb or no terminal.
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	assert.True(t, wantColor(env(nil), true), "control: a terminal gets colour")
	assert.False(t, wantColor(env(map[string]string{"NO_COLOR": "1"}), true), "NO_COLOR")
	assert.False(t, wantColor(env(map[string]string{"TERM": "dumb"}), true), "TERM=dumb")
	assert.False(t, wantColor(env(nil), false), "not a terminal")
	pipeR, pipeW, err := os.Pipe()
	require.NoError(t, err)
	defer func() { _ = pipeR.Close(); _ = pipeW.Close() }()
	opt, err := (&watchFlags{interval: time.Second, since: -1}).options(daemonFlags{}, pipeW, env(nil))
	require.NoError(t, err)
	assert.False(t, opt.tty || opt.color, "a pipe is plain text")
	opt, err = (&watchFlags{interval: time.Second, since: -1, json: true}).options(daemonFlags{}, pipeW, env(nil))
	require.NoError(t, err)
	assert.False(t, opt.color, "--json is never coloured")
	code, plain, _ := runClient(t, t.Context(), d, "watch", "--once", "--since", "0")
	require.Equal(t, 0, code)
	assert.NotContains(t, plain, "\x1b", "plain text without a terminal")
}

// TestTUIFlagPointsToWatch: the terminal UI is gone; `itervox --tui` prints
// one line pointing to itervox watch and exits non-zero (2), before it
// claims any runtime file, with or without --headless.
func TestTUIFlagPointsToWatch(t *testing.T) {
	for _, flags := range [][]string{{"--tui"}, {"--tui", "--headless"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			p := newDaemonProject(t)
			exe, env := p.childCommand(t, flags)
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe)
			cmd.Dir, cmd.Env = p.Dir, env
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr, "stderr: %s", stderr.String())
			assert.Equal(t, tuiRemovedExitCode, exitErr.ExitCode())
			assert.Equal(t, 2, tuiRemovedExitCode)
			assert.Empty(t, stdout.String())
			assert.Equal(t, 1, strings.Count(stderr.String(), tuiRemovedNotice+"\n"), "the notice, once:\n%s", stderr.String())
			assert.Contains(t, tuiRemovedNotice, "itervox watch")
			assert.NotContains(t, stderr.String(), altScreen, "no terminal UI")
			assert.NoFileExists(t, dashboardURLFilePath(p.Workflow), "the daemon never started")
			pidPath, err := pidFilePath(p.Workflow)
			require.NoError(t, err)
			assert.NoFileExists(t, pidPath)
			assert.NoDirExists(t, filepath.Join(p.LogsDir), "nothing was written")
		})
	}
}

// TestWatchReachesAHubProject: with --project the client commands go
// through the hub's proxy (/hub/p/{project}/api/v1/...) with the hub token,
// and the commands watch prints carry --project.
func TestWatchReachesAHubProject(t *testing.T) {
	var mu sync.Mutex
	var paths, auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths, auths = append(paths, r.URL.Path), append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		writeJSONBody(w, map[string]any{"maxConcurrentAgents": 1, "inputRequired": []any{
			map[string]any{"identifier": "API-7", "state": "input_required", "context": "Proceed?"}}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ITERVOX_API_TOKEN", "a-project-token")
	t.Setenv(hubTokenEnv, "a-hub-token")
	var out, errOut strings.Builder
	code := clientCommand(t.Context(), "watch", []string{"--url", srv.URL + "/hub/", "--project", "api", "--once"}, &out, &errOut)
	require.Equal(t, 0, code, errOut.String())
	assert.Equal(t, []string{"/hub/p/api/api/v1/state"}, paths)
	assert.Equal(t, []string{"Bearer a-hub-token"}, auths, "the hub token, never the project's")
	assert.Contains(t, out.String(), "itervox reply --url "+srv.URL+"/hub/ --project api API-7")
}
