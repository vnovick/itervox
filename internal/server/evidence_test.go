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
)

type fakeEvidence struct {
	rep EvidenceReport
	err error
	ids []string
}

func (f *fakeEvidence) IssueEvidence(identifier string) (EvidenceReport, error) {
	f.ids = append(f.ids, identifier)
	return f.rep, f.err
}

// evidenceFixture is a report as the daemon assembles it, unredacted: one
// steered run and an approval from a stored token.
func evidenceFixture(tokenID string) EvidenceReport {
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	return EvidenceReport{
		Runs: []EvidenceRun{{
			RunID: "run-1", Kind: "worker", Profile: "coder", Backend: "claude", Status: "succeeded",
			StartedAt: at, PromptDigest: strings.Repeat("a", 64),
			Steers: []EvidenceSteer{{At: at, Delivery: "live", Text: "use the ghp_0123456789abcdefghijklmnopqrstuvwxyzAB token"}},
			Checks: &EvidenceRunChecks{Evidence: &EvidenceGate{Required: []string{"tests"}, Passed: true, CheckedAt: at}},
		}},
		Approvals: []EvidenceApproval{
			{At: at, Kind: EvidenceApprovalGuardrail, Decision: EvidenceDecisionApproved, Via: "dashboard", Actor: "dashboard:" + tokenID, RunID: "run-1"},
			{At: at, Kind: EvidenceApprovalInput, Decision: EvidenceDecisionAnswered, Via: "tracker", Actor: "tracker:Dana"},
		},
	}
}

func newEvidenceServer(t *testing.T, ev EvidenceClient) (scopedServer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".itervox", apitokens.FileName)
	store := apitokens.Open(path)
	ss := scopedServer{store: store, secrets: map[apitokens.Scope]string{}}
	var approverID string
	for _, sc := range apitokens.Scopes {
		tok, secret, err := store.Create(string(sc)+"-token", sc, nil, "test")
		require.NoError(t, err)
		ss.secrets[sc] = secret
		if sc == apitokens.ScopeOperator {
			approverID = tok.ID
		}
	}
	ss.srv = New(Config{
		Snapshot:    func() StateSnapshot { return StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
		APIToken:    scopesTestBootstrap,
		Tokens:      store,
		Metrics:     http.NotFoundHandler(),
		Evidence:    ev,
	})
	return ss, approverID
}

func getEvidence(t *testing.T, ss scopedServer, token string) (int, EvidenceReport, string) {
	t.Helper()
	rec := doReq(t, ss.srv, http.MethodGet, "/api/v1/issues/ENG-7/evidence", token)
	var rep EvidenceReport
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rep))
	}
	return rec.Code, rep, rec.Body.String()
}

// TestIssueEvidenceRedactsPerScope: a viewer gets the report without
// steering text and without approvers; an operator gets both (steering
// re-redacted); an admin also the stored token's name.
func TestIssueEvidenceRedactsPerScope(t *testing.T) {
	fe := &fakeEvidence{}
	ss, approverID := newEvidenceServer(t, fe)
	fe.rep = evidenceFixture(approverID)

	code, viewer, body := getEvidence(t, ss, ss.secrets[apitokens.ScopeViewer])
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "ENG-7", viewer.Identifier)
	assert.Equal(t, "viewer", viewer.Scope)
	require.Len(t, viewer.Runs, 1)
	require.Len(t, viewer.Runs[0].Steers, 1, "a viewer still sees that a message was delivered")
	assert.Empty(t, viewer.Runs[0].Steers[0].Text)
	assert.Equal(t, "live", viewer.Runs[0].Steers[0].Delivery)
	for _, a := range viewer.Approvals {
		assert.Empty(t, a.Actor)
		assert.Empty(t, a.ActorName)
	}
	assert.NotContains(t, body, "ghp_")
	assert.NotContains(t, body, approverID)
	assert.NotContains(t, body, "Dana")
	assert.NotNil(t, viewer.SignedChecks, "lists are arrays, never null")
	assert.NotNil(t, viewer.PullRequests)
	assert.NotNil(t, viewer.GuardrailBlocks)
	assert.NotNil(t, viewer.Reviews.Verdicts)

	code, op, body := getEvidence(t, ss, ss.secrets[apitokens.ScopeOperator])
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "operator", op.Scope)
	assert.Contains(t, op.Runs[0].Steers[0].Text, "use the")
	assert.NotContains(t, body, "ghp_0123456789", "steering text is redacted again before it is served")
	assert.Equal(t, "dashboard:"+approverID, op.Approvals[0].Actor)
	assert.Empty(t, op.Approvals[0].ActorName, "token names are admin-only")
	assert.Equal(t, "tracker:Dana", op.Approvals[1].Actor)

	code, admin, body := getEvidence(t, ss, ss.secrets[apitokens.ScopeAdmin])
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "admin", admin.Scope)
	assert.Equal(t, "operator-token", admin.Approvals[0].ActorName)
	assert.Empty(t, admin.Approvals[1].ActorName, "a tracker author has no token name")

	assert.Equal(t, []string{"ENG-7", "ENG-7", "ENG-7"}, fe.ids)
	assert.Len(t, fe.rep.Runs[0].Steers[0].Text, len(evidenceFixture(approverID).Runs[0].Steers[0].Text),
		"redaction never writes into the client's report")
}

func TestIssueEvidenceUnavailableAndErrors(t *testing.T) {
	ss, _ := newEvidenceServer(t, nil)
	code, _, body := getEvidence(t, ss, ss.secrets[apitokens.ScopeViewer])
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Contains(t, body, "not_supported")

	ss, _ = newEvidenceServer(t, &fakeEvidence{err: errors.New("disk on fire")})
	code, _, body = getEvidence(t, ss, ss.secrets[apitokens.ScopeViewer])
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Contains(t, body, "evidence_unreadable")

	code, _, _ = getEvidence(t, ss, "")
	assert.Equal(t, http.StatusUnauthorized, code)
}

// TestFeedLeavesActorsOutBelowOperator: GET /api/v1/feed carries who
// answered an approval to operators and admins only.
func TestFeedLeavesActorsOutBelowOperator(t *testing.T) {
	ss := newScopedServer(t)
	ss.srv.dispatch = fakeFeedDispatch{events: []FeedEvent{{Seq: 1, Kind: "guardrail_approved", Identifier: "ENG-7",
		Payload: FeedPayload{Via: "dashboard", Actor: "dashboard:tok_1"}}}}
	for scope, want := range map[apitokens.Scope]string{
		apitokens.ScopeViewer: "", apitokens.ScopeOperator: "dashboard:tok_1", apitokens.ScopeAdmin: "dashboard:tok_1",
	} {
		rec := doReq(t, ss.srv, http.MethodGet, "/api/v1/feed", ss.secrets[scope])
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var page FeedPage
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
		require.Len(t, page.Events, 1)
		assert.Equal(t, want, page.Events[0].Payload.Actor, scope)
		assert.Equal(t, "dashboard", page.Events[0].Payload.Via, scope)
	}
}

type fakeFeedDispatch struct{ events []FeedEvent }

func (f fakeFeedDispatch) Feed(int64, int) FeedPage {
	return FeedPage{Events: f.events, HeadSeq: int64(len(f.events))}
}

func (fakeFeedDispatch) PauseDispatch(context.Context, string, time.Time) (*DispatchPauseRow, error) {
	return nil, nil
}

func (fakeFeedDispatch) ResumeDispatch(context.Context) (*DispatchPauseRow, error) { return nil, nil }

// actorClient records the actor of each human reply and dismissal.
type actorClient struct {
	viaClient
	as []string
}

func (c *actorClient) ProvideInputAs(identifier, message, via, actor string) error {
	c.as = append(c.as, "reply:"+identifier+":"+message+":"+via+":"+actor)
	return nil
}

func (c *actorClient) DismissInputAs(identifier, actor string) error {
	c.as = append(c.as, "dismiss:"+identifier+":"+actor)
	return nil
}

// TestProvideAndDismissInputRecordTheCaller: a human reply or dismissal
// through the API carries the caller's principal (Principal.actor) to the
// daemon; an agent relay (fromAgent) does not.
func TestProvideAndDismissInputRecordTheCaller(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".itervox", apitokens.FileName)
	store := apitokens.Open(path)
	tok, secret, err := store.Create("ops", apitokens.ScopeOperator, nil, "test")
	require.NoError(t, err)
	g := &agentInputGuardrails{}
	c := &actorClient{}
	srv := New(Config{
		Snapshot:    func() StateSnapshot { return StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
		APIToken:    scopesTestBootstrap,
		Tokens:      store,
		Client:      c,
		Guardrails:  g,
		Metrics:     http.NotFoundHandler(),
	})
	post := func(path, body, token string) int {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		req.Host = "127.0.0.1:8090"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusAccepted, post("/api/v1/issues/ENG-1/provide-input", `{"message":"approve"}`, secret))
	require.Equal(t, http.StatusAccepted, post("/api/v1/issues/ENG-1/provide-input", `{"message":"approve","via":"slack"}`, scopesTestBootstrap))
	require.Equal(t, http.StatusAccepted, post("/api/v1/issues/ENG-1/provide-input", `{"message":"yes","fromAgent":true}`, secret))
	require.Equal(t, http.StatusAccepted, post("/api/v1/issues/ENG-1/dismiss-input", `{}`, secret))
	assert.Equal(t, []string{
		"reply:ENG-1:approve::dashboard:" + tok.ID,
		"reply:ENG-1:approve:slack:dashboard:bootstrap",
		"dismiss:ENG-1:dashboard:" + tok.ID,
	}, c.as)
	assert.Equal(t, []string{"ENG-1:yes"}, g.agent, "an agent relay never carries a person's actor")
	assert.Empty(t, c.via, "the actor path replaces the via-only path")
}
