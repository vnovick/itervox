package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/config"
)

// fakePlaybooks is a PlaybooksClient over fixed data.
type fakePlaybooks struct {
	set       map[string]string
	busy      bool
	createErr error
	created   int
}

func (f *fakePlaybooks) Playbooks() PlaybooksResponse {
	return PlaybooksResponse{Enabled: true, Playbooks: []PlaybookRow{{Name: "bug-fix", Description: "Reproduce first.", Keywords: []string{"bug"}, RequireEvidence: []string{"test"}}}}
}

func (f *fakePlaybooks) SetIssuePlaybook(identifier, name string) error {
	if f.busy {
		return ErrBusy
	}
	if name != "" && name != PlaybookNone && name != "bug-fix" {
		return ErrUnknownPlaybook
	}
	f.set[identifier] = name
	return nil
}

func (f *fakePlaybooks) Decisions(string) []DecisionRunView {
	return []DecisionRunView{{Path: ".itervox/handoff/x.decisions.tsv", Role: "implementer",
		Decisions: []DecisionRowView{{Time: "2026-05-01T09:00:00Z", Decision: "Reproduce first"}}}}
}

func (f *fakePlaybooks) CreateVerifySkillIssue(context.Context) (VerifySkillIssueResult, error) {
	f.created++
	if f.createErr != nil {
		return VerifySkillIssueResult{}, f.createErr
	}
	return VerifySkillIssueResult{Identifier: "ENG-9", URL: "https://example.test/ENG-9"}, nil
}

func doBody(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
	req.Host = "127.0.0.1:8090"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestPlaybookEndpointsScopesAndErrors (#218): the playbook list, decision
// logs and steering phrases are viewer reads; the per-issue override and
// the verify-skill issue need operator; errors map to their statuses.
func TestPlaybookEndpointsScopesAndErrors(t *testing.T) {
	store := apitokens.Open(filepath.Join(t.TempDir(), ".itervox", apitokens.FileName))
	secrets := map[apitokens.Scope]string{}
	for _, sc := range apitokens.Scopes {
		_, secret, err := store.Create(string(sc)+"-token", sc, nil, "test")
		require.NoError(t, err)
		secrets[sc] = secret
	}
	fp := &fakePlaybooks{set: map[string]string{}}
	srv := New(Config{
		Snapshot: func() StateSnapshot { return StateSnapshot{} }, RefreshChan: make(chan struct{}, 1),
		APIToken: scopesTestBootstrap, Tokens: store, Playbooks: fp,
		SteeringPhrases: []config.SteeringPhrase{{Label: "Prove it works", Message: "Show me the test output."}},
	})
	viewer, operator := secrets[apitokens.ScopeViewer], secrets[apitokens.ScopeOperator]

	rec := doBody(t, srv, http.MethodGet, "/api/v1/playbooks", viewer, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var list PlaybooksResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Equal(t, "bug-fix", list.Playbooks[0].Name)

	rec = doBody(t, srv, http.MethodGet, "/api/v1/steering/phrases", viewer, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"phrases":[{"label":"Prove it works","message":"Show me the test output."}]}`, rec.Body.String())

	rec = doBody(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/decisions", viewer, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var dec DecisionsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &dec))
	assert.Equal(t, "ENG-1", dec.Identifier)
	assert.Equal(t, "Reproduce first", dec.Runs[0].Decisions[0].Decision)

	// The override: viewer refused, operator accepted, unknown 400, busy 503.
	rec = doBody(t, srv, http.MethodPost, "/api/v1/issues/ENG-1/playbook", viewer, `{"playbook":"bug-fix"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, fp.set)
	rec = doBody(t, srv, http.MethodPost, "/api/v1/issues/ENG-1/playbook", operator, `{"playbook":"bug-fix"}`)
	assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Equal(t, "bug-fix", fp.set["ENG-1"])
	rec = doBody(t, srv, http.MethodPost, "/api/v1/issues/ENG-1/playbook", operator, `{"playbook":"nope"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "unknown_playbook")
	fp.busy = true
	rec = doBody(t, srv, http.MethodPost, "/api/v1/issues/ENG-1/playbook", operator, `{"playbook":""}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	// The verify-skill issue: operator only; a tracker failure is a 502
	// whose message is redacted.
	rec = doBody(t, srv, http.MethodPost, "/api/v1/repo/readiness/verify-skill-issue", viewer, "{}")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Zero(t, fp.created)
	rec = doBody(t, srv, http.MethodPost, "/api/v1/repo/readiness/verify-skill-issue", operator, "{}")
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.JSONEq(t, `{"identifier":"ENG-9","url":"https://example.test/ENG-9"}`, rec.Body.String())
	secret := "ghp_" + strings.Repeat("Q", 36)
	fp.createErr = errors.New("tracker said no for " + secret)
	rec = doBody(t, srv, http.MethodPost, "/api/v1/repo/readiness/verify-skill-issue", operator, "{}")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.NotContains(t, rec.Body.String(), secret)

	// Not wired: 503, except the phrases, which are config.
	bare := New(Config{Snapshot: func() StateSnapshot { return StateSnapshot{} }, RefreshChan: make(chan struct{}, 1)})
	assert.Equal(t, http.StatusServiceUnavailable, doReq(t, bare, http.MethodGet, "/api/v1/playbooks", "").Code)
	assert.Equal(t, http.StatusOK, doReq(t, bare, http.MethodGet, "/api/v1/steering/phrases", "").Code)
}
