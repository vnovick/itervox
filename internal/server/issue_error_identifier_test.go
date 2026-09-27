package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestIssueControlErrorsNameTheIssue pins BH-M5-5: the dashboard toast store
// dedupes by message+variant, so an issue-control failure whose message does
// not name the issue merges failures for different issues into one toast.
// Every issue-scoped failure message must carry the identifier, and two
// different issues must produce two different messages.
func TestIssueControlErrorsNameTheIssue(t *testing.T) {
	notWaiting := errors.New("no pending input")
	cases := []struct {
		name     string
		client   func() *server.FuncClient
		path     string
		body     string
		agentAct string // non-empty: call through the token-gated agent-actions route
		method   string // default POST
		status   int
		code     string
	}{
		{
			name: "provide_input_not_waiting",
			client: func() *server.FuncClient {
				return &server.FuncClient{ProvideInputFn: func(string, string) error { return notWaiting }}
			},
			path: "/api/v1/issues/%s/provide-input", body: `{"message":"yes"}`,
			status: http.StatusNotFound, code: "not_found",
		},
		{
			name: "dismiss_input_not_waiting",
			client: func() *server.FuncClient {
				return &server.FuncClient{DismissInputFn: func(string) error { return notWaiting }}
			},
			path:   "/api/v1/issues/%s/dismiss-input",
			status: http.StatusNotFound, code: "not_found",
		},
		{
			name: "agent_provide_input_not_waiting",
			client: func() *server.FuncClient {
				return &server.FuncClient{ProvideInputFn: func(string, string) error { return notWaiting }}
			},
			path: "/api/v1/agent-actions/%s/provide-input", body: `{"message":"yes"}`,
			agentAct: config.AgentActionProvideInput,
			status:   http.StatusNotFound, code: "not_found",
		},
		{
			name: "comment_issue_not_found",
			client: func() *server.FuncClient {
				return &server.FuncClient{PostOperatorCommentFn: func(context.Context, string, string) (bool, error) {
					return false, tracker.ErrNotFound
				}}
			},
			path: "/api/v1/issues/%s/comment", body: `{"body":"hi"}`,
			status: http.StatusNotFound, code: "not_found",
		},
		{
			name: "comment_failed",
			client: func() *server.FuncClient {
				return &server.FuncClient{PostOperatorCommentFn: func(context.Context, string, string) (bool, error) {
					return false, errors.New("tracker returned 500")
				}}
			},
			path: "/api/v1/issues/%s/comment", body: `{"body":"hi"}`,
			status: http.StatusInternalServerError, code: "comment_failed",
		},
		{
			name: "update_state_failed",
			client: func() *server.FuncClient {
				return &server.FuncClient{UpdateIssueStateFn: func(context.Context, string, string) error {
					return errors.New("unknown state")
				}}
			},
			path: "/api/v1/issues/%s/state", body: `{"state":"Done"}`, method: http.MethodPatch,
			status: http.StatusInternalServerError, code: "update_failed",
		},
		{
			name: "ai_review_dispatch_failed",
			client: func() *server.FuncClient {
				return &server.FuncClient{DispatchReviewerFn: func(string) error { return errors.New("no reviewer profile") }}
			},
			path:   "/api/v1/issues/%s/ai-review",
			status: http.StatusInternalServerError, code: "dispatch_failed",
		},
		{
			name: "backend_pin_refused",
			client: func() *server.FuncClient {
				return &server.FuncClient{CheckIssueBackendPinFn: func(string, string) error {
					return errors.New(`the command runs "claude"; kept "claude"`)
				}}
			},
			path: "/api/v1/issues/%s/backend", body: `{"backend":"codex"}`,
			status: http.StatusConflict, code: "backend_pin_refused",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := map[string]string{}
			for _, id := range []string{"ENG-1", "ENG-2"} {
				cfg := makeTestConfig(baseSnap())
				cfg.Client = tc.client()
				var token string
				if tc.agentAct != "" {
					store := agentactions.NewStore()
					var err error
					token, err = store.Issue(id, "run-1", []string{tc.agentAct}, "", time.Minute)
					require.NoError(t, err)
					cfg.ActionTokenStore = store
				}
				srv := server.New(cfg)
				method := tc.method
				if method == "" {
					method = http.MethodPost
				}
				req := httptest.NewRequest(method, strings.Replace(tc.path, "%s", id, 1), bytes.NewBufferString(tc.body))
				req.Header.Set("Content-Type", "application/json")
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				srv.ServeHTTP(w, req)
				require.Equalf(t, tc.status, w.Code, "body: %s", w.Body.String())
				var resp struct {
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tc.code, resp.Error.Code)
				assert.Containsf(t, resp.Error.Message, id, "message %q does not name the issue", resp.Error.Message)
				msgs[id] = resp.Error.Message
			}
			assert.NotEqual(t, msgs["ENG-1"], msgs["ENG-2"], "failures for different issues must not share a message")
		})
	}
}

// The wrap keeps the error chain (errors.Is) and never names the issue twice.
func TestIssueControlErrorWrapKeepsChain(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{PostOperatorCommentFn: func(context.Context, string, string) (bool, error) {
		return false, errors.New("comment on ENG-7 rejected")
	}}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/issues/ENG-7/comment", `{"body":"hi"}`)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, 1, strings.Count(w.Body.String(), "ENG-7"), "body: %s", w.Body.String())
}
