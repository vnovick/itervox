//go:build unix

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// fakeModelServer is an httptest server standing in for a local model
// server; any HTTP answer counts for the probe.
func fakeModelServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// fakeServerAt serves handler on 127.0.0.1 (httptest picks the port) and
// returns the port; never a real model server.
func fakeServerAt(t *testing.T, handler http.HandlerFunc) int {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	return port
}

// modelList answers /v1/models with one model owned by ownedBy and 404s
// everything else.
func modelList(ownedBy string, ids ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		var items []string
		for _, id := range ids {
			items = append(items, `{"id":"`+id+`","object":"model","owned_by":"`+ownedBy+`"}`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[` + strings.Join(items, ",") + `]}`))
	}
}

// closedPort is a port nothing listens on any more.
func closedPort(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	_, portText, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	srv.Close()
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	return port
}

// withCandidates points quickstart's detection at ports (restored after).
func withCandidates(t *testing.T, candidates ...modelServerCandidate) {
	t.Helper()
	prev := quickstartModelServerCandidates
	quickstartModelServerCandidates = candidates
	t.Cleanup(func() { quickstartModelServerCandidates = prev })
}

// TestDetectModelServers: every candidate that answers HTTP is listed, in
// candidate order, with what its answer looks like (owned_by, or Ollama's
// root text) and its first models; a port nothing listens on is not.
func TestDetectModelServers(t *testing.T) {
	ollama := fakeServerAt(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte("Ollama is running"))
			return
		}
		http.NotFound(w, r)
	})
	studio := fakeServerAt(t, modelList("organization_owner", "a", "b", "c", "d"))
	llama := fakeServerAt(t, modelList("llamacpp", "model.gguf"))
	vllm := fakeServerAt(t, modelList("vllm", "org/coder"))
	unknown := fakeServerAt(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	gone := closedPort(t)

	got := detectModelServers([]modelServerCandidate{
		{Port: ollama, Server: "Ollama"}, {Port: gone, Server: "Nothing"}, {Port: studio, Server: "LM Studio"},
		{Port: llama, Server: "llama.cpp server"}, {Port: vllm, Server: "vLLM"}, {Port: unknown, Server: "Something"},
	})
	require.Len(t, got, 5)
	assert.Equal(t, "Ollama", got[0].Kind)
	assert.Equal(t, "http://127.0.0.1:"+strconv.Itoa(ollama), got[0].URL)
	assert.Equal(t, "LM Studio", got[1].Kind)
	assert.Equal(t, []string{"a", "b", "c"}, got[1].Models, "capped")
	assert.Equal(t, "llama.cpp server", got[2].Kind)
	assert.Equal(t, "vLLM", got[3].Kind)
	assert.Equal(t, "http://127.0.0.1:"+strconv.Itoa(vllm)+" — vLLM (models: org/coder)", got[3].describe())
	assert.Empty(t, got[4].Kind)
	assert.Equal(t, "http://127.0.0.1:"+strconv.Itoa(unknown)+" — answers HTTP; the usual port of Something", got[4].describe())
}

// TestDetectModelServersDoesNotFollowRedirects: a candidate answering
// with a redirect counts as an answer, and the probe never requests the
// redirect's target (it could be off the machine).
func TestDetectModelServersDoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
	}))
	t.Cleanup(target.Close)
	redirecting := fakeServerAt(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/models", http.StatusFound)
	})
	got := detectModelServers([]modelServerCandidate{{Port: redirecting, Server: "Something"}})
	require.Len(t, got, 1, "a redirect is an answer")
	assert.Equal(t, "http://127.0.0.1:"+strconv.Itoa(redirecting), got[0].URL)
	assert.Zero(t, targetHits.Load(), "the redirect was followed")
}

// TestDetectModelServersOnIPv6Loopback: a server listening on [::1] only
// is found, with its [::1] URL. Skipped where the machine has no IPv6
// loopback.
func TestDetectModelServersOnIPv6Loopback(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	srv := httptest.NewUnstartedServer(modelList("llamacpp", "model.gguf"))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	port := ln.Addr().(*net.TCPAddr).Port
	got := detectModelServers([]modelServerCandidate{{Port: port, Server: "llama.cpp server"}})
	require.Len(t, got, 1)
	assert.Equal(t, "http://[::1]:"+strconv.Itoa(port), got[0].URL)
	assert.Equal(t, "llama.cpp server", got[0].Kind)
}

// TestModelServerFromAnswer: a list number, a port, host:port and a URL;
// codex gets /v1 on what Itervox builds; nonsense is refused.
func TestModelServerFromAnswer(t *testing.T) {
	detected := []detectedModelServer{{URL: "http://127.0.0.1:4100"}, {URL: "http://127.0.0.1:4200"}}
	for answer, want := range map[string]string{
		"":                          "",
		"2":                         "http://127.0.0.1:4200",
		"4300":                      "http://127.0.0.1:4300",
		"gpu.lan:4400":              "http://gpu.lan:4400",
		"https://gpu.lan:4500/base": "https://gpu.lan:4500/base",
	} {
		got, err := modelServerFromAnswer(answer, detected, config.BackendClaude)
		require.NoError(t, err, answer)
		assert.Equal(t, want, got, answer)
	}
	got, err := modelServerFromAnswer("1", detected, config.BackendCodex)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:4100/v1", got)
	got, err = modelServerFromAnswer("4300", nil, config.BackendCodex)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:4300/v1", got)
	for _, bad := range []string{"0", "70000", "gpu.lan", "yes please"} {
		_, err := modelServerFromAnswer(bad, detected, config.BackendClaude)
		assert.Error(t, err, bad)
	}
}

// TestQuickstartLocalModelNonInteractive: --model-server probes the server
// and writes a `local` profile with model_server (no pool written: it is
// derived, and has no port in it), whose workflow loads and whose runs use
// the claude variable.
func TestQuickstartLocalModelNonInteractive(t *testing.T) {
	dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
	url := fakeModelServer(t)

	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: dir, NoStart: true, Yes: true, ModelServer: url}, strings.NewReader(""), &out)
	require.Equal(t, 0, code, out.String())
	assert.Contains(t, out.String(), url+" is answering")
	assert.Contains(t, out.String(), "added the `local` profile (claude, model_server "+url+", pool model-local)")
	assert.NotContains(t, out.String(), "Model servers answering", "no detection without a prompt")

	raw, err := os.ReadFile(wf)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "model_server: \""+url+"\"")
	assert.NotContains(t, string(raw), "pool:", "the derived pool is not written")

	cfg, err := config.Load(wf)
	require.NoError(t, err)
	local := cfg.Agent.Profiles["local"]
	assert.Equal(t, url, local.ModelServerURL)
	assert.Equal(t, "model-local", local.Pool)
	assert.True(t, local.PoolAuto)
	assert.Equal(t, "ANTHROPIC_BASE_URL="+url, config.ProfileModelServerEnv(local, "claude", cfg.Agent.Command))
	assert.FileExists(t, dir+"/.itervox/agents/local/SOUL.md")

	// Re-running leaves an existing `local` profile alone.
	out.Reset()
	code = quickstart(quickstartOptions{Dir: dir, NoStart: true, Yes: true, ModelServer: "http://127.0.0.1:9"}, strings.NewReader(""), &out)
	require.Equal(t, 0, code, out.String())
	assert.Contains(t, out.String(), "a \"local\" profile already exists")
	again, err := config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, url, again.Agent.Profiles["local"].ModelServerURL)
}

// TestQuickstartLocalModelSkipped: --yes alone and --no-local-model never
// add a profile (and do not probe: the candidate answers, but nothing is
// listed); an interactive "no", an empty answer or EOF does not either.
func TestQuickstartLocalModelSkipped(t *testing.T) {
	answering := fakeServerAt(t, modelList("vllm", "m"))
	for name, tc := range map[string]struct {
		opts       quickstartOptions
		input      string
		candidates []modelServerCandidate
	}{
		"yes alone":       {opts: quickstartOptions{Yes: true}, candidates: []modelServerCandidate{{Port: answering, Server: "vLLM"}}},
		"no-local-model":  {opts: quickstartOptions{NoLocalModel: true}, candidates: []modelServerCandidate{{Port: answering, Server: "vLLM"}}},
		"declined":        {input: "n\n"},
		"eof":             {},
		"detected, empty": {input: "\n", candidates: []modelServerCandidate{{Port: answering, Server: "vLLM"}}},
		"detected, eof":   {candidates: []modelServerCandidate{{Port: answering, Server: "vLLM"}}},
		"no port typed":   {input: "y\n\n"},
	} {
		t.Run(name, func(t *testing.T) {
			withCandidates(t, tc.candidates...)
			dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
			opts := tc.opts
			opts.Dir, opts.NoStart = dir, true
			var out strings.Builder
			require.Equal(t, 0, quickstart(opts, strings.NewReader(tc.input), &out), out.String())
			if opts.Yes || opts.NoLocalModel {
				assert.NotContains(t, out.String(), "Model servers answering")
			}
			cfg, err := config.Load(wf)
			require.NoError(t, err)
			assert.NotContains(t, cfg.Agent.Profiles, "local")
		})
	}
}

// TestQuickstartLocalModelPickDetected: the detected servers are listed and
// a number picks one; the profile gets its URL, and the hint names a model
// it serves.
func TestQuickstartLocalModelPickDetected(t *testing.T) {
	first := fakeServerAt(t, modelList("library", "small"))
	second := fakeServerAt(t, modelList("vllm", "org/coder"))
	withCandidates(t, modelServerCandidate{Port: first, Server: "Ollama"}, modelServerCandidate{Port: second, Server: "vLLM"})
	dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)

	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader("2\n"), &out)
	require.Equal(t, 0, code, out.String())
	secondURL := "http://127.0.0.1:" + strconv.Itoa(second)
	assert.Contains(t, out.String(), "Model servers answering on this machine:\n  1) http://127.0.0.1:"+strconv.Itoa(first)+" — Ollama (models: small)\n  2) "+secondURL+" — vLLM (models: org/coder)\n")
	assert.Contains(t, out.String(), "Enter its number, or another port, host:port or URL (empty skips)")
	assert.Contains(t, out.String(), secondURL+" is answering")
	assert.Contains(t, out.String(), "(--model org/coder)")
	cfg, err := config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, secondURL, cfg.Agent.Profiles["local"].ModelServer)
	assert.Equal(t, "model-local", cfg.Agent.Profiles["local"].Pool)
}

// TestQuickstartLocalModelTyped: with servers detected, a typed port that is
// not one of them is probed; with none detected, the step says which ports
// it tried, asks, and takes a port (no default) — a bad answer is asked
// again; a server that does not answer asks before writing.
func TestQuickstartLocalModelTyped(t *testing.T) {
	t.Run("detected, another port typed", func(t *testing.T) {
		listed := fakeServerAt(t, modelList("vllm", "m"))
		other := fakeServerAt(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
		withCandidates(t, modelServerCandidate{Port: listed, Server: "vLLM"})
		dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
		var out strings.Builder
		code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader(strconv.Itoa(other)+"\n"), &out)
		require.Equal(t, 0, code, out.String())
		cfg, err := config.Load(wf)
		require.NoError(t, err)
		assert.Equal(t, "http://127.0.0.1:"+strconv.Itoa(other), cfg.Agent.Profiles["local"].ModelServer)
		assert.Contains(t, out.String(), "(--model <name>)")
	})
	t.Run("none detected, port typed after a bad answer", func(t *testing.T) {
		gone := closedPort(t)
		withCandidates(t, modelServerCandidate{Port: gone, Server: "vLLM"})
		up := fakeServerAt(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
		dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
		var out strings.Builder
		code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader("y\nhttp://127.0.0.1\n"+strconv.Itoa(up)+"\n"), &out)
		require.Equal(t, 0, code, out.String())
		assert.Contains(t, out.String(), "No local model server answered on the usual ports ("+strconv.Itoa(gone)+" vLLM).")
		assert.Contains(t, out.String(), "Use a local model server for a `local` profile? [y/N]")
		assert.Contains(t, out.String(), "Model server port, host:port or URL (empty skips): ")
		assert.NotContains(t, out.String(), "[http", "no default URL is offered")
		assert.Contains(t, out.String(), "has no port; write the port the server listens on")
		cfg, err := config.Load(wf)
		require.NoError(t, err)
		assert.Equal(t, "http://127.0.0.1:"+strconv.Itoa(up), cfg.Agent.Profiles["local"].ModelServer)
	})
	t.Run("typed URL not answering, declined", func(t *testing.T) {
		dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
		prev := probeModelServer
		var probed string
		probeModelServer = func(u string) error { probed = u; return assert.AnError }
		t.Cleanup(func() { probeModelServer = prev })
		var out strings.Builder
		code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader("y\n4321\nn\n"), &out)
		require.Equal(t, 0, code, out.String())
		assert.Equal(t, "http://127.0.0.1:4321", probed, "a bare port is on the loopback address")
		assert.Contains(t, out.String(), "is not answering")
		assert.Contains(t, out.String(), "Add the `local` profile anyway?")
		cfg, err := config.Load(wf)
		require.NoError(t, err)
		assert.NotContains(t, cfg.Agent.Profiles, "local")
	})
}

// TestQuickstartModelServerFlagValidation: a malformed --model-server, one
// without an explicit port, and the two flags together are refused before
// anything runs.
func TestQuickstartModelServerFlagValidation(t *testing.T) {
	_, err := parseQuickstartFlags([]string{"--model-server", "127.0.0.1:4321"})
	require.Error(t, err)
	for _, noPort := range []string{"http://127.0.0.1", "https://gpu.lan/v1"} {
		_, err = parseQuickstartFlags([]string{"--model-server", noPort})
		require.Error(t, err, noPort)
		assert.Contains(t, err.Error(), "has no port; write the port the server listens on", noPort)
	}
	_, err = parseQuickstartFlags([]string{"--model-server", "http://127.0.0.1:4321", "--no-local-model"})
	require.Error(t, err)
	opts, err := parseQuickstartFlags([]string{"--model-server", "http://127.0.0.1:4321"})
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:4321", opts.ModelServer)
}
