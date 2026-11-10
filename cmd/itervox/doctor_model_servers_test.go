package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestModelServerURLsReadsCommandAssignments: only assignments before the
// program count, through an optional `env` prefix, quotes stripped.
func TestModelServerURLsReadsCommandAssignments(t *testing.T) {
	assert.Equal(t, map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:8000"},
		modelServerURLs("ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude --model glm"))
	assert.Equal(t, map[string]string{"OPENAI_BASE_URL": "http://localhost:8000/v1"},
		modelServerURLs(`env -i OPENAI_BASE_URL="http://localhost:8000/v1" opencode`))
	assert.Empty(t, modelServerURLs("claude ANTHROPIC_BASE_URL=http://x"), "after the program it is an argument")
	assert.Empty(t, modelServerURLs("FOO=1 claude"))
}

// TestDoctorWarnsWhenAModelServerDoesNotAnswer: doctor probes each model
// server a profile command points at, lists who uses it, and warns when it
// does not answer — without changing the exit code.
func TestDoctorWarnsWhenAModelServerDoesNotAnswer(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound) // any HTTP answer counts
	}))
	defer up.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	cfg := &config.Config{}
	cfg.Agent.Command = "claude"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"local":     {Command: "ANTHROPIC_BASE_URL=" + up.URL + " claude"},
		"local-alt": {Command: "ANTHROPIC_BASE_URL=" + up.URL + " claude --model other"},
		"stopped":   {Command: "OPENAI_BASE_URL=" + downURL + "/v1 opencode"},
		"from-var":  {Command: "ANTHROPIC_BASE_URL=$LOCAL_MODEL_URL claude"},
		"off":       {Command: "ANTHROPIC_BASE_URL=" + downURL + " claude", Enabled: new(false)},
	}
	c := checkModelServers(cfg)
	require.Len(t, c.Servers, 3, "one row per server; disabled profiles skipped")

	var b strings.Builder
	renderModelServerCheck(&b, c)
	out := b.String()
	assert.Contains(t, out, "\nModel servers\n")
	assert.Contains(t, out, "ANTHROPIC_BASE_URL="+up.URL+", used by local, local-alt: answering")
	assert.Contains(t, out, "OPENAI_BASE_URL="+downURL+"/v1, used by stopped: not answering")
	assert.Contains(t, out, "WARNING: stopped point their agent at "+downURL+"/v1")
	assert.Contains(t, out, "coli serve")
	assert.Contains(t, out, "ANTHROPIC_BASE_URL=$LOCAL_MODEL_URL, used by from-var: set from a variable, not checked")
	assert.Equal(t, 0, doctorExitCode(DoctorReport{SchemaPassed: true, ModelServers: c}))

	b.Reset()
	renderModelServerCheck(&b, checkModelServers(&config.Config{}))
	assert.Empty(t, b.String(), "no model server configured: no section")
}

// TestModelServerRejectsNonHTTPURL: a value that is not an http(s) URL is
// reported without a request.
func TestModelServerRejectsNonHTTPURL(t *testing.T) {
	prev := probeModelServer
	t.Cleanup(func() { probeModelServer = prev })
	probeModelServer = func(string) error { return errors.New("must not be called") }
	cfg := &config.Config{}
	cfg.Agent.Command = "ANTHROPIC_BASE_URL=127.0.0.1:8000 claude"
	c := checkModelServers(cfg)
	require.Len(t, c.Servers, 1)
	assert.EqualError(t, c.Servers[0].Err, "not an http(s) URL")
}
