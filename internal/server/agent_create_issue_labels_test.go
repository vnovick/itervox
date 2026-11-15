package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestCreateIssueLabelsMustBeAllowed (#253): every label a create_issue call
// asks for must be in the grant's create_issue_labels allow-list (ignoring
// case; the allow-list spelling is what reaches the tracker). Anything else —
// including any label under an empty allow-list — is refused with 403
// agent_action_denied before the tracker is called. A blank label is a 400.
// blocked_by_source passes through.
func TestCreateIssueLabelsMustBeAllowed(t *testing.T) {
	store := agentactions.NewStore()
	allowed, err := store.IssueWithScope("ENG-1", "run-1", []string{config.AgentActionCreateIssue},
		agentactions.Scope{CreateIssueState: "Todo", CreateIssueLabels: []string{"repo:web", "needs-design"}}, time.Minute)
	require.NoError(t, err)
	none, err := store.IssueWithScope("ENG-1", "run-2", []string{config.AgentActionCreateIssue},
		agentactions.Scope{CreateIssueState: "Todo"}, time.Minute)
	require.NoError(t, err)

	var calls []tracker.NewIssue
	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.Client = &server.FuncClient{
		CreateIssueFn: func(_ context.Context, identifier string, in tracker.NewIssue) (*domain.Issue, error) {
			assert.Equal(t, "ENG-1", identifier)
			calls = append(calls, in)
			return &domain.Issue{Identifier: "ENG-2", Title: in.Title, State: in.State, Labels: in.Labels}, nil
		},
	}
	srv := server.New(cfg)
	post := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/create-issue", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}
	errorBody := func(w *httptest.ResponseRecorder) (code, message string) {
		var resp struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
		return resp.Error.Code, resp.Error.Message
	}

	w := post(allowed, `{"title":"Web follow-up","labels":["repo:web","repo:mobile"]}`)
	assert.Equal(t, http.StatusForbidden, w.Code)
	code, msg := errorBody(w)
	assert.Equal(t, "agent_action_denied", code)
	assert.Equal(t, "label_not_allowed:repo:mobile", msg)

	w = post(none, `{"title":"Web follow-up","labels":["repo:web"]}`)
	assert.Equal(t, http.StatusForbidden, w.Code, "an empty allow-list allows no label")
	code, msg = errorBody(w)
	assert.Equal(t, "agent_action_denied", code)
	assert.Equal(t, "label_not_allowed:repo:web", msg)

	w = post(allowed, `{"title":"Web follow-up","labels":["  "]}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, calls, "no refused request reaches the tracker")

	w = post(allowed, `{"title":"Web follow-up","body":"Wire it","labels":["Repo:Web","repo:web","needs-design"],"blocked_by_source":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, calls, 1)
	assert.Equal(t, tracker.NewIssue{Title: "Web follow-up", Body: "Wire it", State: "Todo",
		Labels: []string{"repo:web", "needs-design"}, BlockedBySource: true}, calls[0])

	w = post(none, `{"title":"Plain follow-up"}`)
	require.Equal(t, http.StatusOK, w.Code, "no labels needs no allow-list")
	require.Len(t, calls, 2)
	assert.Equal(t, tracker.NewIssue{Title: "Plain follow-up", State: "Todo"}, calls[1])
}
