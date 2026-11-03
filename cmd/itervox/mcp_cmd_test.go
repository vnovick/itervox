package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/mcpserver"
	"github.com/vnovick/itervox/internal/memory"
	"github.com/vnovick/itervox/internal/server"
)

// rpcLine is one JSON-RPC message of a scripted MCP client.
func rpcLine(t *testing.T, id int, method string, params any) string {
	t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if id > 0 {
		msg["id"] = id
	}
	if params != nil {
		msg["params"] = params
	}
	raw, err := json.Marshal(msg)
	require.NoError(t, err)
	return string(raw) + "\n"
}

// rpcResponses parses every stdout line as a JSON-RPC response.
func rpcResponses(t *testing.T, out string) []map[string]any {
	t.Helper()
	var resps []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), "stdout carries protocol messages only: %q", sc.Text())
		require.Equal(t, "2.0", m["jsonrpc"])
		resps = append(resps, m)
	}
	return resps
}

// toolText is a tools/call response's text and isError.
func toolText(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	require.Nil(t, resp["error"])
	res := resp["result"].(map[string]any)
	isErr, _ := res["isError"].(bool)
	return res["content"].([]any)[0].(map[string]any)["text"].(string), isErr
}

// callMCPTool runs one tools/call through a fresh MCP session on client.
func callMCPTool(t *testing.T, client *mcpserver.Client, name string, args map[string]any) (string, bool) {
	t.Helper()
	in := rpcLine(t, 1, "initialize", map[string]any{"protocolVersion": "2025-06-18"}) +
		rpcLine(t, 0, "notifications/initialized", nil) +
		rpcLine(t, 2, "tools/call", map[string]any{"name": name, "arguments": args})
	var out bytes.Buffer
	srv := &mcpserver.Server{API: client}
	require.NoError(t, srv.Serve(context.Background(), strings.NewReader(in), &out))
	resps := rpcResponses(t, out.String())
	require.Len(t, resps, 2)
	return toolText(t, resps[1])
}

type recalled struct {
	Entries []struct {
		ID     string `json:"id"`
		Text   string `json:"text"`
		Pinned bool   `json:"pinned"`
	} `json:"entries"`
	Budget  int `json:"budget"`
	Tokens  int `json:"tokens"`
	Dropped int `json:"dropped"`
}

func (r recalled) ids() []string {
	var out []string
	for _, e := range r.Entries {
		out = append(out, e.ID)
	}
	return out
}

// TestMCPRecallUsesRetrievalRanking (#216): the recall tool returns exactly
// the selection a dispatched run makes (memory.Resolve: pinned first, then
// lexical rank, active and in-profile entries only, within the budget), in
// that order — through GET /api/v1/memory/recall and the real adapter.
func TestMCPRecallUsesRetrievalRanking(t *testing.T) {
	f := newMemoryAPIFixture(t)
	ts := httptest.NewServer(f.srv)
	t.Cleanup(ts.Close)
	add := func(body map[string]any) string {
		var e server.MemoryEntryView
		f.do(t, http.MethodPost, "/api/v1/memory", body, http.StatusCreated, &e)
		return e.ID
	}
	// Created least relevant last, so the newest-first tie-break (the order
	// without the query) differs from the relevance order.
	db := add(map[string]any{"text": "The staging database is read-only on Fridays.", "tags": []string{"database"}, "scope": "project"})
	deploys := add(map[string]any{"text": "Staging deploys go through the release pipeline."})
	npm := add(map[string]any{"text": "Use pnpm, never npm."})
	pinned := add(map[string]any{"text": "Ask before touching billing code."})
	f.do(t, http.MethodPost, "/api/v1/memory/"+pinned+"/pin", nil, http.StatusOK, nil)
	reviewerOnly := add(map[string]any{"text": "Reviewers check staging database migrations.", "profiles": []string{"reviewer"}})
	proposed := add(map[string]any{"text": "The staging database needs a VPN.", "status": "proposed"})

	const query = "staging database"
	want := func(budget int) memory.Selection {
		sel, errs := memory.Resolve(f.wf, memory.Target{Profile: memory.DefaultProfile, Title: query}, budget)
		require.Empty(t, errs)
		return sel
	}
	client := mcpserver.NewClient(ts.URL, "")

	text, isErr := callMCPTool(t, client, "recall", map[string]any{"query": query})
	require.False(t, isErr, text)
	var got recalled
	require.NoError(t, json.Unmarshal([]byte(text), &got))
	full := want(2000)
	assert.Equal(t, full.IDs(), got.ids(), "the run-time order")
	assert.Equal(t, []string{pinned, db, deploys, npm}, got.ids(), "pinned first, then by relevance; never another profile's or a proposed entry")
	assert.NotContains(t, got.ids(), reviewerOnly)
	assert.NotContains(t, got.ids(), proposed)
	assert.Equal(t, 2000, got.Budget, "the run-time memory budget by default")
	assert.Equal(t, full.Tokens, got.Tokens)

	// A tight budget drops the lowest-ranked entries, as at run time.
	small := full.Tokens - 10
	text, isErr = callMCPTool(t, client, "recall", map[string]any{"query": query, "budget": small})
	require.False(t, isErr, text)
	got = recalled{}
	require.NoError(t, json.Unmarshal([]byte(text), &got))
	sel := want(small)
	assert.Equal(t, sel.IDs(), got.ids())
	assert.Equal(t, sel.Dropped, got.Dropped)
	assert.Positive(t, got.Dropped)

	// The reviewer profile sees its own entry.
	text, _ = callMCPTool(t, client, "recall", map[string]any{"query": query, "profile": "reviewer"})
	got = recalled{}
	require.NoError(t, json.Unmarshal([]byte(text), &got))
	assert.Contains(t, got.ids(), reviewerOnly)
}

// TestMCPRememberCreatesProposedMemoryOnly (#216): remember creates one
// proposed entry (the lessons review queue), redacted, with mcp
// provenance; it never reaches a run, and a caller cannot ask for active.
func TestMCPRememberCreatesProposedMemoryOnly(t *testing.T) {
	f := newMemoryAPIFixture(t)
	ts := httptest.NewServer(f.srv)
	t.Cleanup(ts.Close)
	client := mcpserver.NewClient(ts.URL, "")
	secret := "ghp_" + strings.Repeat("B", 36)

	text, isErr := callMCPTool(t, client, "remember", map[string]any{"text": "Staging DB is read-only.", "status": "active"})
	require.True(t, isErr, "a status argument is refused")
	assert.Contains(t, text, "invalid arguments")

	text, isErr = callMCPTool(t, client, "remember", map[string]any{
		"text": "Staging DB is read-only; the old token was " + secret, "tags": []string{"database"}, "issue": "ENG-4",
	})
	require.False(t, isErr, text)

	var view server.MemoryView
	f.do(t, http.MethodGet, "/api/v1/memory", nil, http.StatusOK, &view)
	require.Len(t, view.Entries, 1, "exactly one entry")
	e := view.Entries[0]
	assert.Equal(t, memory.StatusProposed, e.Status, "never active directly")
	assert.Equal(t, "mcp", e.Source.Kind)
	assert.Equal(t, "ENG-4", e.Source.Issue)
	assert.Equal(t, memory.ScopePrivate, e.Scope, "memory.lessons.scope (default private)")
	assert.NotContains(t, e.Text, secret)
	assert.Contains(t, text, e.ID)

	sel, _ := memory.Resolve(f.wf, memory.Target{Title: "staging database read-only", Labels: []string{"database"}}, 2000)
	assert.Empty(t, sel.Entries, "a proposed entry never reaches a run")
}

// mcpTestDaemon is a real server in token mode with a token store, found
// through .itervox/dashboard_url like a running daemon.
func mcpTestDaemon(t *testing.T) (dir, wf string, store *apitokens.Store) {
	t.Helper()
	dir = t.TempDir()
	wf = filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(wf, []byte("---\n---\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox"), 0o755))
	store = apitokens.Open(apitokens.Path(wf))
	srv := server.New(server.Config{
		Snapshot: func() server.StateSnapshot {
			return server.StateSnapshot{
				Running:      []server.RunningRow{{Identifier: "ENG-1", TurnCount: 2, LastEvent: "raw agent output"}},
				ActiveStates: []string{"Todo"},
				Paused:       []string{},
			}
		},
		RefreshChan: make(chan struct{}, 1),
		APIToken:    "bootstrap-admin-token-0123456789abcdef",
		Tokens:      store,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	require.NoError(t, os.WriteFile(dashboardURLFilePath(wf), []byte(ts.URL+"/\n"), 0o644))
	return dir, wf, store
}

func mcpTestEnv(t *testing.T, vars map[string]string) mcpEnv {
	return mcpEnv{
		getenv:      func(k string) string { return vars[k] },
		registryDir: t.TempDir(),
		scan:        func() []int { return nil },
		cwd:         func(int) string { return "" },
	}
}

// captureLogs sends slog output to a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestMCPStdioRoundTrip (#216): `itervox mcp` with a scripted client over
// stdin/stdout against a real server in token mode: it finds the daemon of
// the project, creates a viewer token named "mcp" through the token store
// and remembers it in a 0600 gitignored file (never printing or logging
// the secret), answers initialize, tools/list (read tools only) and
// tools/call, writes nothing but protocol to stdout, and reuses the token
// next time; --token-scope operator replaces it with an operator token.
func TestMCPStdioRoundTrip(t *testing.T) {
	dir, wf, store := mcpTestDaemon(t)
	logs := captureLogs(t)
	script := rpcLine(t, 1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "scripted", "version": "1"}}) +
		rpcLine(t, 0, "notifications/initialized", nil) +
		rpcLine(t, 2, "tools/list", map[string]any{}) +
		rpcLine(t, 3, "tools/call", map[string]any{"name": "board_summary", "arguments": map[string]any{}}) +
		rpcLine(t, 4, "tools/call", map[string]any{"name": "dispatch", "arguments": map[string]any{"identifier": "ENG-2"}}) +
		rpcLine(t, 5, "ping", nil)
	run := func(args ...string) ([]map[string]any, string) {
		var out, errOut bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		code := mcpCmd(ctx, args, strings.NewReader(script), &out, &errOut, mcpTestEnv(t, nil))
		require.Equal(t, 0, code, errOut.String())
		return rpcResponses(t, out.String()), out.String() + errOut.String()
	}
	names := func(resp map[string]any) []string {
		var out []string
		for _, tl := range resp["result"].(map[string]any)["tools"].([]any) {
			out = append(out, tl.(map[string]any)["name"].(string))
		}
		return out
	}

	resps, printed := run("--project", dir)
	require.Len(t, resps, 5, "one response per request; none for the notification")
	for i, r := range resps {
		assert.EqualValues(t, i+1, r["id"])
	}
	assert.Equal(t, "2025-06-18", resps[0]["result"].(map[string]any)["protocolVersion"])
	assert.Equal(t, mcpserver.ToolNames("viewer"), names(resps[1]), "a viewer lists no write tools")
	text, isErr := toolText(t, resps[2])
	require.False(t, isErr, text)
	assert.Contains(t, text, "ENG-1")
	assert.NotContains(t, text, "raw agent output")
	text, isErr = toolText(t, resps[3])
	assert.True(t, isErr)
	assert.Contains(t, text, "needs an operator token")
	assert.Equal(t, map[string]any{}, resps[4]["result"])

	tokens, err := store.List()
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.Equal(t, "mcp", tokens[0].Name)
	assert.Equal(t, apitokens.ScopeViewer, tokens[0].Scope)
	info, err := os.Stat(mcpTokenPath(wf))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	var remembered mcpTokenFile
	raw, err := os.ReadFile(mcpTokenPath(wf))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &remembered))
	assert.Equal(t, tokens[0].ID, remembered.ID)
	assert.True(t, strings.HasPrefix(remembered.Secret, apitokens.SecretPrefix))
	assert.NotContains(t, printed, remembered.Secret, "the secret is never printed")
	assert.NotContains(t, logs.String(), remembered.Secret, "the secret is never logged")
	gi, err := os.ReadFile(filepath.Join(dir, ".itervox", ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, strings.Split(string(gi), "\n"), "/mcp-token.json")

	// Second run: the remembered token is reused.
	resps, _ = run("--project", dir)
	require.Len(t, resps, 5)
	tokens, err = store.List()
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.Equal(t, remembered.ID, tokens[0].ID)

	// --token-scope operator: an operator token replaces the viewer one.
	resps, printed = run("--workflow", wf, "--token-scope", "operator")
	assert.Equal(t, mcpserver.ToolNames("operator"), names(resps[1]))
	tokens, err = store.List()
	require.NoError(t, err)
	require.Len(t, tokens, 1, "the replaced mcp token is revoked")
	assert.Equal(t, apitokens.ScopeOperator, tokens[0].Scope)
	assert.NotEqual(t, remembered.ID, tokens[0].ID)
	raw, err = os.ReadFile(mcpTokenPath(wf))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &remembered))
	assert.NotContains(t, printed, remembered.Secret)
}

// TestMCPCommandTokenAndDiscovery: an explicit ITERVOX_TOKEN is used as
// given (no stored token is created), --token-scope admin is refused, and
// a project without a daemon is an error that names it.
func TestMCPCommandTokenAndDiscovery(t *testing.T) {
	dir, _, store := mcpTestDaemon(t)
	_ = captureLogs(t)
	script := rpcLine(t, 1, "tools/list", map[string]any{})
	var out, errOut bytes.Buffer
	env := mcpTestEnv(t, map[string]string{mcpTokenEnv: "bootstrap-admin-token-0123456789abcdef"})
	require.Equal(t, 0, mcpCmd(context.Background(), []string{"--project", dir}, strings.NewReader(script), &out, &errOut, env), errOut.String())
	resps := rpcResponses(t, out.String())
	require.Len(t, resps, 1)
	assert.Len(t, resps[0]["result"].(map[string]any)["tools"], len(mcpserver.ToolNames("admin")), "an explicit token keeps its own scope")
	tokens, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, tokens, "no stored token is created for an explicit token")

	errOut.Reset()
	assert.Equal(t, 1, mcpCmd(context.Background(), []string{"--project", dir, "--token-scope", "admin"}, strings.NewReader(""), &out, &errOut, env))
	assert.Contains(t, errOut.String(), "viewer or operator")

	errOut.Reset()
	empty := t.TempDir()
	assert.Equal(t, 1, mcpCmd(context.Background(), []string{"--project", empty}, strings.NewReader(""), &out, &errOut, mcpTestEnv(t, nil)))
	assert.Contains(t, errOut.String(), "no running itervox daemon for "+empty)
}

// TestMCPFindsTheProjectDaemonInTheRegistry: the daemon is selected from
// the host registry `itervox status --json` reads, by project directory,
// with its workflow and whether it needs a token.
func TestMCPFindsTheProjectDaemonInTheRegistry(t *testing.T) {
	root := t.TempDir()
	regDir := filepath.Join(root, "daemons")
	require.NoError(t, os.MkdirAll(regDir, 0o700))
	for i, name := range []string{"alpha", "beta"} {
		wf := filepath.Join(root, name, "WORKFLOW.md")
		require.NoError(t, os.MkdirAll(filepath.Join(root, name, ".itervox"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, name, ".itervox", "daemon.pid"),
			[]byte(strconv.Itoa(os.Getpid())+"\t"+wf+"\n"), 0o600))
		rec := daemonRecord{SchemaVersion: 1, PID: os.Getpid(), Workflow: wf, ProjectDir: filepath.Dir(wf),
			URL: "http://127.0.0.1:" + strconv.Itoa(9100+i) + "/", TokenRequired: name == "alpha"}
		raw, err := json.Marshal(rec)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(regDir, strconv.Itoa(100+i)+".json"), raw, 0o600))
	}
	env := mcpTestEnv(t, nil)
	env.registryDir = regDir

	got, err := findMCPDaemon(filepath.Join(root, "beta"), "", env)
	require.NoError(t, err)
	assert.Equal(t, mcpTarget{base: "http://127.0.0.1:9101", workflow: filepath.Join(root, "beta", "WORKFLOW.md"), tokenRequired: false}, got)
	got, err = findMCPDaemon(filepath.Join(root, "alpha"), "", env)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:9100", got.base)
	assert.True(t, got.tokenRequired)
	got, err = findMCPDaemon(filepath.Join(root, "alpha"), "http://example.test:1/?token=x", env)
	require.NoError(t, err)
	assert.Equal(t, "http://example.test:1", got.base, "--url wins; a tokenised query is dropped")
}
