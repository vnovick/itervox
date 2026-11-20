//go:build unix

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// TestQuickstartLocalModelNonInteractive: --model-server probes the server
// and writes a `local` profile with model_server (no pool written: it is
// derived), whose workflow loads and whose runs use the claude variable.
func TestQuickstartLocalModelNonInteractive(t *testing.T) {
	dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
	url := fakeModelServer(t)

	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: dir, NoStart: true, Yes: true, ModelServer: url}, strings.NewReader(""), &out)
	require.Equal(t, 0, code, out.String())
	assert.Contains(t, out.String(), url+" is answering")
	assert.Contains(t, out.String(), "added the `local` profile (claude, model_server "+url)

	raw, err := os.ReadFile(wf)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "model_server: \""+url+"\"")
	assert.NotContains(t, string(raw), "pool:", "the derived pool is not written")

	cfg, err := config.Load(wf)
	require.NoError(t, err)
	local := cfg.Agent.Profiles["local"]
	assert.Equal(t, url, local.ModelServerURL)
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
// add a profile; an interactive "no" (or EOF) does not either.
func TestQuickstartLocalModelSkipped(t *testing.T) {
	for name, tc := range map[string]struct {
		opts  quickstartOptions
		input string
	}{
		"yes alone":      {opts: quickstartOptions{Yes: true}},
		"no-local-model": {opts: quickstartOptions{NoLocalModel: true}},
		"declined":       {input: "n\n"},
		"eof":            {},
	} {
		t.Run(name, func(t *testing.T) {
			dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
			opts := tc.opts
			opts.Dir, opts.NoStart = dir, true
			var out strings.Builder
			require.Equal(t, 0, quickstart(opts, strings.NewReader(tc.input), &out), out.String())
			cfg, err := config.Load(wf)
			require.NoError(t, err)
			assert.NotContains(t, cfg.Agent.Profiles, "local")
		})
	}
}

// TestQuickstartLocalModelInteractive: yes, then the default URL (empty
// answer) or a typed one; a server that does not answer asks before
// writing.
func TestQuickstartLocalModelInteractive(t *testing.T) {
	t.Run("typed URL", func(t *testing.T) {
		dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
		url := fakeModelServer(t)
		var out strings.Builder
		code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader("y\n"+url+"\n"), &out)
		require.Equal(t, 0, code, out.String())
		assert.Contains(t, out.String(), "Use a local model server for a `local` profile? [y/N]")
		assert.Contains(t, out.String(), "Model server URL ["+quickstartDefaultModelServer+"]")
		cfg, err := config.Load(wf)
		require.NoError(t, err)
		assert.Equal(t, url, cfg.Agent.Profiles["local"].ModelServer)
	})
	t.Run("default URL not answering, declined", func(t *testing.T) {
		dir, wf, _ := quickstartGitHubProject(t, wantStateLabels)
		prev := probeModelServer
		var probed string
		probeModelServer = func(u string) error { probed = u; return assert.AnError }
		t.Cleanup(func() { probeModelServer = prev })
		var out strings.Builder
		code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader("y\n\nn\n"), &out)
		require.Equal(t, 0, code, out.String())
		assert.Equal(t, quickstartDefaultModelServer, probed, "the empty answer takes the default")
		assert.Contains(t, out.String(), "is not answering")
		assert.Contains(t, out.String(), "Add the `local` profile anyway?")
		cfg, err := config.Load(wf)
		require.NoError(t, err)
		assert.NotContains(t, cfg.Agent.Profiles, "local")
	})
}

// TestQuickstartModelServerFlagValidation: a malformed --model-server and
// the two flags together are refused before anything runs.
func TestQuickstartModelServerFlagValidation(t *testing.T) {
	_, err := parseQuickstartFlags([]string{"--model-server", "127.0.0.1:8000"})
	require.Error(t, err)
	_, err = parseQuickstartFlags([]string{"--model-server", "http://127.0.0.1:8000", "--no-local-model"})
	require.Error(t, err)
	opts, err := parseQuickstartFlags([]string{"--model-server", "http://127.0.0.1:8000"})
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:8000", opts.ModelServer)
}
