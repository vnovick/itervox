package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/guardrails"
)

// Server support for `itervox mcp` (#216): the memory recall endpoint and
// the provide-input fromAgent flag.

type recallMemoryClient struct {
	MemoryClient // nil: only RecallMemory is called
	query        string
	profile      string
	budget       int
}

func (m *recallMemoryClient) RecallMemory(query, profile string, budget int) MemoryRecallView {
	m.query, m.profile, m.budget = query, profile, budget
	return MemoryRecallView{Enabled: true, Profile: "default", Budget: 2000}
}

// TestMemoryRecallEndpoint: GET /api/v1/memory/recall is open to viewer
// tokens, validates q and budget, and passes them to the memory client.
func TestMemoryRecallEndpoint(t *testing.T) {
	store := apitokens.Open(filepath.Join(t.TempDir(), ".itervox", apitokens.FileName))
	_, viewer, err := store.Create("v", apitokens.ScopeViewer, nil, "test")
	require.NoError(t, err)
	mem := &recallMemoryClient{}
	srv := New(Config{
		Snapshot:    func() StateSnapshot { return StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
		APIToken:    scopesTestBootstrap,
		Tokens:      store,
		Memory:      mem,
	})

	rec := doReq(t, srv, http.MethodGet, "/api/v1/memory/recall?q=staging+db&profile=reviewer&budget=300", viewer)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "staging db", mem.query)
	assert.Equal(t, "reviewer", mem.profile)
	assert.Equal(t, 300, mem.budget)
	var v MemoryRecallView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v))
	assert.NotNil(t, v.Entries, "entries is [] when empty, never null")

	for _, bad := range []string{"", "?q=", "?q=x&budget=0", "?q=x&budget=8001", "?q=x&budget=abc", "?q=" + strings.Repeat("a", 2001)} {
		rec := doReq(t, srv, http.MethodGet, "/api/v1/memory/recall"+bad, viewer)
		assert.Equal(t, http.StatusBadRequest, rec.Code, bad)
	}
}

type agentInputGuardrails struct {
	GuardrailsClient
	agent []string
}

func (g *agentInputGuardrails) ProvideAgentInput(identifier, message string) error {
	g.agent = append(g.agent, identifier+":"+message)
	return nil
}

func (g *agentInputGuardrails) GuardrailApprovalStatus(string, string, string) guardrails.ApprovalStatus {
	return guardrails.ApprovalStatus{}
}

// TestProvideInputFromAgentUsesAgentPath: POST provide-input with
// fromAgent:true goes through the agent path, which the event loop never
// lets answer a guardrail approval; without it the human path is used.
func TestProvideInputFromAgentUsesAgentPath(t *testing.T) {
	var human []string
	g := &agentInputGuardrails{}
	srv := New(Config{
		Snapshot:    func() StateSnapshot { return StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
		Client: &FuncClient{ProvideInputFn: func(id, msg string) error {
			human = append(human, id+":"+msg)
			return nil
		}},
		Guardrails: g,
	})
	post := func(body string) int {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/issues/ENG-1/provide-input", strings.NewReader(body))
		req.Host = "127.0.0.1:8090"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusAccepted, post(`{"message":"yes","fromAgent":true}`))
	assert.Equal(t, []string{"ENG-1:yes"}, g.agent)
	assert.Empty(t, human)

	require.Equal(t, http.StatusAccepted, post(`{"message":"approve"}`))
	assert.Equal(t, []string{"ENG-1:approve"}, human)
	assert.Len(t, g.agent, 1)
}
