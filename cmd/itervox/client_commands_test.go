package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #120 — terminal commands that replace the TUI.

const clientTestToken = "client-test-token"

// apiCall is one request a fake daemon received.
type apiCall struct {
	method, path, auth, body string
}

// fakeDaemon is an API server for the client commands: handler answers each
// request; every call is recorded.
type fakeDaemon struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []apiCall
}

func newFakeDaemon(t *testing.T, handler http.HandlerFunc) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		d.mu.Lock()
		d.calls = append(d.calls, apiCall{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), string(raw)})
		d.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(d.srv.Close)
	t.Setenv("ITERVOX_API_TOKEN", clientTestToken)
	return d
}

func (d *fakeDaemon) recorded() []apiCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]apiCall(nil), d.calls...)
}

func runClient(t *testing.T, ctx context.Context, d *fakeDaemon, name string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	code := clientCommand(ctx, name, append([]string{"--url", d.srv.URL}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func writeJSONBody(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// TestWatchRendersRunningAndQueuedIssues: watch shows the running, retrying,
// queued, input-required and paused issues of a live daemon's state.
func TestWatchRendersRunningAndQueuedIssues(t *testing.T) {
	state := map[string]any{
		"maxConcurrentAgents": 3,
		"running": []any{map[string]any{
			"identifier": "ENG-1", "state": "In Progress", "backend": "claude", "kind": "reviewer",
			"turnCount": 4, "tokens": 48200, "elapsedMs": 754000, "lastEvent": "running tests\nok",
		}},
		"retrying":         []any{map[string]any{"identifier": "ENG-2", "attempt": 2, "dueAt": time.Now().Add(90 * time.Second), "error": "exit 1"}},
		"inputRequired":    []any{map[string]any{"identifier": "ENG-3", "state": "input_required", "context": "Which database?"}},
		"automationQueue":  []any{map[string]any{"identifier": "ENG-4", "automationId": "nightly-triage", "reason": "no_slots"}},
		"paused":           []any{"ENG-5"},
		"pauseReasons":     map[string]any{"ENG-5": "retries_exhausted"},
		"dispatchPressure": map[string]any{"eligibleWaiting": 2},
	}
	d := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/state", r.URL.Path)
		writeJSONBody(w, state)
	})
	code, out, errOut := runClient(t, t.Context(), d, "watch", "--once")
	require.Equal(t, 0, code, errOut)
	for _, want := range []string{
		"1/3 agents · 2 waiting for a slot · 1 retrying · 1 waiting for input · 1 queued automations · 1 paused",
		"RUNNING", "ENG-1", "In Progress", "claude reviewer", "12m34s", "turn 4", "48.2k tok", "running tests ok",
		"RETRYING", "ENG-2", "attempt 2", "in 1m",
		"WAITING FOR INPUT", "ENG-3", "Which database?",
		"QUEUED AUTOMATIONS", "ENG-4", "nightly-triage", "no slots",
		"PAUSED", "ENG-5", "retries exhausted",
	} {
		assert.Contains(t, out, want)
	}
	calls := d.recorded()
	require.Len(t, calls, 1)
	assert.Equal(t, "Bearer "+clientTestToken, calls[0].auth)
}

// TestWatchRefreshesUntilStopped: without --once, watch keeps reading the
// state; without a terminal it prints a view only when it changes, and a
// running agent's ticking elapsed time and token count are not a change.
func TestWatchRefreshesUntilStopped(t *testing.T) {
	var mu sync.Mutex
	running := false
	polls := 0
	d := newFakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		polls++
		list := []any{}
		if running {
			list = append(list, map[string]any{"identifier": "ENG-9", "state": "Todo",
				"elapsedMs": polls * 1000, "tokens": polls * 100})
		}
		writeJSONBody(w, map[string]any{"maxConcurrentAgents": 2, "running": list})
	})
	ctx, cancel := context.WithCancel(t.Context())
	var out strings.Builder
	done := make(chan int)
	go func() {
		done <- clientCommand(ctx, "watch", []string{"--url", d.srv.URL, "--interval", "20ms"}, &out, io.Discard)
	}()
	require.Eventually(t, func() bool { return len(d.recorded()) >= 3 }, 2*time.Second, 5*time.Millisecond)
	mu.Lock()
	running = true
	mu.Unlock()
	n := len(d.recorded())
	require.Eventually(t, func() bool { return len(d.recorded()) >= n+4 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	require.Equal(t, 0, <-done)
	got := out.String()
	assert.Equal(t, 2, strings.Count(got, "--- "), "printed twice: the first view and the changed one:\n%s", got)
	assert.Contains(t, got, "Nothing running or waiting.")
	assert.Contains(t, got, "ENG-9")
}

// TestLogsFollowStreamsNewLines: logs -f prints each line the daemon
// streams, keeps following, and resumes after a dropped connection from the
// last line it saw.
func TestLogsFollowStreamsNewLines(t *testing.T) {
	var mu sync.Mutex
	var lastIDs []string
	conn := 0
	d := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/issues/ENG-1/log-stream", r.URL.Path)
		mu.Lock()
		conn++
		n := conn
		lastIDs = append(lastIDs, r.Header.Get("Last-Event-ID"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		frame := func(seq int, msg string) {
			_, _ = fmt.Fprintf(w, "id: 1-%d\nevent: log\ndata: {\"level\":\"info\",\"message\":%q,\"time\":\"10:00:0%d\"}\n\n", seq, msg, seq)
			fl.Flush()
		}
		if n == 1 {
			frame(1, "turn 1 started")
			time.Sleep(30 * time.Millisecond)
			frame(2, "editing main.go")
			return // the connection drops
		}
		frame(3, "turn 1 done")
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	var mu2 sync.Mutex
	var out strings.Builder
	w := writerFunc(func(p []byte) (int, error) { mu2.Lock(); defer mu2.Unlock(); return out.Write(p) })
	done := make(chan int)
	go func() { done <- clientCommand(ctx, "logs", []string{"--url", d.srv.URL, "ENG-1", "-f"}, w, io.Discard) }()
	require.Eventually(t, func() bool {
		mu2.Lock()
		defer mu2.Unlock()
		return strings.Contains(out.String(), "turn 1 done")
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	require.Equal(t, 0, <-done)
	mu2.Lock()
	got := out.String()
	mu2.Unlock()
	assert.Equal(t, "10:00:01 INFO  turn 1 started\n10:00:02 INFO  editing main.go\n10:00:03 INFO  turn 1 done\n", got)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"", "1-2"}, lastIDs, "the reconnect resumes after the last line")
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// TestLogsPrintsTheRunLog: without -f, logs prints the issue's log and exits.
func TestLogsPrintsTheRunLog(t *testing.T) {
	d := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/issues/%2312/logs", r.URL.EscapedPath(), "a GitHub identifier is escaped")
		writeJSONBody(w, []any{map[string]any{"level": "warn", "message": "retrying", "tool": "Bash", "time": "09:00:00"}})
	})
	code, out, _ := runClient(t, t.Context(), d, "logs", "#12")
	require.Equal(t, 0, code)
	assert.Equal(t, "09:00:00 WARN  [Bash] retrying\n", out)
}

// TestQuickActionsCallTheirAPI: each quick action calls its API route with
// the token and reports the result; a refusal is reported with the API's
// message and exit 1.
func TestQuickActionsCallTheirAPI(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		calls []apiCall // method, path, body (auth checked separately)
		out   string
	}{
		{"pause", []string{"ENG-1"}, []apiCall{{method: "POST", path: "/api/v1/issues/ENG-1/cancel"}}, "Paused ENG-1; its agent was stopped.\n"},
		{"resume", []string{"ENG-1"}, []apiCall{{method: "POST", path: "/api/v1/issues/ENG-1/resume"}}, "Resumed ENG-1.\n"},
		{"dispatch", []string{"ENG-1"}, []apiCall{
			{method: "GET", path: "/api/v1/state"},
			{method: "PATCH", path: "/api/v1/issues/ENG-1/state", body: `{"state":"Todo"}`},
		}, "Moved ENG-1 to Todo; it is dispatched when a slot is free.\n"},
		{"workers", []string{"4"}, []apiCall{{method: "POST", path: "/api/v1/settings/workers", body: `{"workers":4}`}}, "Workers: 4.\n"},
		{"reply", []string{"ENG-1", "use", "postgres"}, []apiCall{
			{method: "POST", path: "/api/v1/issues/ENG-1/provide-input", body: `{"message":"use postgres"}`},
		}, "Reply to ENG-1 queued; its agent resumes with it if the issue is waiting for input (itervox watch shows those).\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/state":
					writeJSONBody(w, map[string]any{"activeStates": []string{"Todo", "In Progress"}})
				case "/api/v1/settings/workers":
					writeJSONBody(w, map[string]any{"workers": 4})
				default:
					writeJSONBody(w, map[string]any{"ok": true})
				}
			})
			code, out, errOut := runClient(t, t.Context(), d, tc.name, tc.args...)
			require.Equal(t, 0, code, errOut)
			assert.Equal(t, tc.out, out)
			calls := d.recorded()
			require.Len(t, calls, len(tc.calls))
			for i, want := range tc.calls {
				assert.Equal(t, want.method, calls[i].method)
				assert.Equal(t, want.path, calls[i].path)
				if want.body != "" {
					assert.JSONEq(t, want.body, calls[i].body)
				}
				assert.Equal(t, "Bearer "+clientTestToken, calls[i].auth)
			}
		})
	}

	t.Run("refused", func(t *testing.T) {
		d := newFakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"not_running","message":"issue ENG-1 is not running"}}`)
		})
		code, out, errOut := runClient(t, t.Context(), d, "pause", "ENG-1")
		assert.Equal(t, 1, code)
		assert.Empty(t, out)
		assert.Equal(t, "itervox pause: issue ENG-1 is not running (404)\n", errOut)
	})

	t.Run("usage", func(t *testing.T) {
		d := newFakeDaemon(t, func(http.ResponseWriter, *http.Request) {})
		for _, args := range [][]string{{"pause"}, {"workers", "0"}, {"reply", "ENG-1"}, {"logs"}} {
			code, _, _ := runClient(t, t.Context(), d, args[0], args[1:]...)
			assert.Equal(t, 2, code, "%v", args)
		}
		assert.Empty(t, d.recorded(), "a usage error sends nothing")
	})
}

// TestClientFindsTheDaemonAndItsToken: without --url the commands read
// .itervox/dashboard_url; the token comes from ITERVOX_API_TOKEN, then
// .itervox/.env, then <logs-dir>/api-token.
func TestClientFindsTheDaemonAndItsToken(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "WORKFLOW.md")
	logs := filepath.Join(dir, "logs")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox"), 0o755))
	require.NoError(t, os.MkdirAll(logs, 0o755))
	f := daemonFlags{workflow: wf, logsDir: logs}

	t.Setenv("ITERVOX_API_TOKEN", "")
	_, err := f.client()
	require.ErrorIs(t, err, errNoDaemon)

	require.NoError(t, os.WriteFile(dashboardURLFilePath(wf), []byte("http://127.0.0.1:8123/?token=abc\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(logs, apiTokenFileName), []byte("from-file\n"), 0o600))
	c, err := f.client()
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:8123", c.base, "the URL without its token")
	assert.Equal(t, "from-file", c.token)

	require.NoError(t, os.WriteFile(filepath.Join(dir, ".itervox", ".env"), []byte("# pinned\nITERVOX_API_TOKEN=\"from-env-file\"\n"), 0o600))
	c, err = f.client()
	require.NoError(t, err)
	assert.Equal(t, "from-env-file", c.token, ".itervox/.env wins over api-token")

	t.Setenv("ITERVOX_API_TOKEN", "from-environment")
	c, err = f.client()
	require.NoError(t, err)
	assert.Equal(t, "from-environment", c.token, "the environment wins")
}

// altScreen is the escape sequence the terminal UI starts with.
const altScreen = "\x1b[?1049h"

// TestHeadlessOnATTYDoesNotStartTheTUIOrPrintTheToken: with --headless the
// daemon, on a real terminal, starts no terminal UI and prints the dashboard
// URL without the token; without it (the control) the UI starts.
func TestHeadlessOnATTYDoesNotStartTheTUIOrPrintTheToken(t *testing.T) {
	const announced = "dashboard URL ("
	t.Run("--headless", func(t *testing.T) {
		p := newDaemonProject(t)
		ptyOut, _, _ := p.startUnderPTY(t, `exec "$ITERVOX_TEST_EXE" >"$ITERVOX_TEST_OUT"`, []string{"--headless"})
		waitFor(t, 30*time.Second, "the dashboard announcement on the terminal", func() bool {
			return countFileOccurrences(ptyOut, announced) >= 1
		})
		token := readTrim(t, filepath.Join(p.LogsDir, apiTokenFileName))
		time.Sleep(time.Second) // time for a TUI to have started, if it were going to
		got := readTrim(t, ptyOut)
		assert.NotContains(t, got, token, "the token is not printed")
		assert.Contains(t, got, "token withheld")
		assert.Contains(t, got, "--headless or --no-print-token was passed", "the hint names --headless")
		assert.NotContains(t, got, "***", "the hint survives the log redactor")
		assert.NotContains(t, got, altScreen, "no terminal UI")
		assert.Contains(t, got, "orchestrator", "the logs go to stderr")
	})
	t.Run("control: with --tui the UI starts", func(t *testing.T) {
		p := newDaemonProject(t)
		ptyOut, _, _ := p.startUnderPTY(t, `exec "$ITERVOX_TEST_EXE" >"$ITERVOX_TEST_OUT"`, []string{"--tui"})
		waitFor(t, 30*time.Second, "the terminal UI on the terminal", func() bool {
			return countFileOccurrences(ptyOut, altScreen) >= 1
		})
	})
}

// TestDefaultStartIsHeadless (#134): with no flag, the daemon on a real
// terminal starts no terminal UI, logs on stderr and prints the dashboard
// URL; stderr being a terminal, the URL carries the token.
func TestDefaultStartIsHeadless(t *testing.T) {
	p := newDaemonProject(t)
	ptyOut, _, _ := p.startUnderPTY(t, `exec "$ITERVOX_TEST_EXE" >"$ITERVOX_TEST_OUT"`, nil)
	waitFor(t, 30*time.Second, "the dashboard URL on the terminal", func() bool {
		return countFileOccurrences(ptyOut, "dashboard URL (") >= 1
	})
	token := readTrim(t, filepath.Join(p.LogsDir, apiTokenFileName))
	time.Sleep(time.Second) // time for a TUI to have started, if it were going to
	got := readTrim(t, ptyOut)
	assert.NotContains(t, got, altScreen, "no terminal UI")
	assert.Contains(t, got, "?token="+token, "the dashboard URL is printed")
	assert.Contains(t, got, "orchestrator", "the logs go to stderr")
	assert.NotContains(t, got, "terminal UI is deprecated", "no deprecation warning without --tui")
}

// TestTUIFlagStartsTheTUI (#134): --tui starts the terminal UI on a
// terminal, which shows the deprecation notice in its header, and logs that
// it is deprecated; without a terminal it warns and runs headless.
func TestTUIFlagStartsTheTUI(t *testing.T) {
	t.Run("on a terminal", func(t *testing.T) {
		p := newDaemonProject(t)
		// A sized terminal: script(1)'s is 0x0, where the UI never draws.
		ptyOut, _, _ := p.startUnderPTY(t, `stty cols 200 rows 50; exec "$ITERVOX_TEST_EXE" >"$ITERVOX_TEST_OUT"`, []string{"--tui"})
		waitFor(t, 30*time.Second, "the terminal UI on the terminal", func() bool {
			return countFileOccurrences(ptyOut, altScreen) >= 1
		})
		waitFor(t, 30*time.Second, "the deprecation notice in the UI's header", func() bool {
			return countFileOccurrences(ptyOut, "deprecated and will be removed in v0.3") >= 1
		})
		waitFor(t, 10*time.Second, "the deprecation warning in the log file", func() bool {
			return countFileOccurrences(filepath.Join(p.LogsDir, "itervox.log"), "the terminal UI is deprecated") >= 1
		})
	})
	t.Run("without a terminal", func(t *testing.T) {
		p := newDaemonProject(t)
		_, stderr := p.startHeadless(t, []string{"--tui"})
		waitFor(t, 30*time.Second, "the warning on stderr", func() bool {
			return countFileOccurrences(stderr, "--tui: no terminal is attached") >= 1
		})
		assert.Zero(t, countFileOccurrences(stderr, altScreen), "no terminal UI")
	})
}
