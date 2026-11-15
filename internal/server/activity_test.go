package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/sysmon"
)

type fakeActivity struct {
	rep     sysmon.ActivityReport
	err     error
	gotRun  string
	gotIssu string
}

func (f *fakeActivity) Activity(identifier, runID string) (sysmon.ActivityReport, error) {
	f.gotIssu, f.gotRun = identifier, runID
	return f.rep, f.err
}

// activityServer is a token-mode server with every scope's token, the
// fake activity client and a run context carrying resources.
func activityServer(t *testing.T, act ActivityClient) scopedServer {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".itervox", apitokens.FileName)
	store := apitokens.Open(path)
	secrets := make(map[apitokens.Scope]string)
	for _, sc := range apitokens.Scopes {
		_, secret, err := store.Create(string(sc)+"-token", sc, nil, "test")
		require.NoError(t, err)
		secrets[sc] = secret
	}
	usage := &sysmon.RunUsage{Identifier: "ENG-1", Live: true, RSSBytes: 1 << 20, Samples: 1,
		TopCommands: []sysmon.ChildCommand{{PID: 9, Name: "git", Args: "push origin feature/x"}}}
	srv := New(Config{
		Snapshot: func() StateSnapshot {
			return StateSnapshot{Running: []RunningRow{{Identifier: "ENG-1", Resources: usage}}}
		},
		RefreshChan: make(chan struct{}, 1),
		APIToken:    scopesTestBootstrap,
		Tokens:      store,
		Activity:    act,
		Changes:     &fakeChanges{run: &RunContextRow{RunID: "run-1", Live: true, Resources: usage}},
	})
	return scopedServer{srv: srv, store: store, secrets: secrets}
}

// TestIssueActivityIsOperatorAndReadsTheRun: viewers are refused; an
// operator gets the report (empty lists as [], not null) for ?run=; an
// unknown run is 404 no_activity; no client is 503.
func TestIssueActivityIsOperatorAndReadsTheRun(t *testing.T) {
	act := &fakeActivity{rep: sysmon.ActivityReport{Identifier: "ENG-1", RunID: "run-1", Mode: sysmon.ModeSampled,
		Commands: []sysmon.CommandRecord{{Name: "git", Args: "status", Count: 2}}}}
	ss := activityServer(t, act)

	rec := doReq(t, ss.srv, http.MethodGet, "/api/v1/issues/ENG-1/activity?run=run-1", ss.secrets[apitokens.ScopeViewer])
	require.Equal(t, http.StatusForbidden, rec.Code, "command lines and paths need operator")

	rec = doReq(t, ss.srv, http.MethodGet, "/api/v1/issues/ENG-1/activity?run=run-1", ss.secrets[apitokens.ScopeOperator])
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "ENG-1", act.gotIssu)
	assert.Equal(t, "run-1", act.gotRun)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "sampled", body["mode"])
	assert.Equal(t, []any{}, body["files"], "an empty list is [] for the schema")
	assert.Equal(t, []any{}, body["connections"])
	assert.Equal(t, []any{}, body["notes"])

	act.err = sysmon.ErrActivityNotFound
	rec = doReq(t, ss.srv, http.MethodGet, "/api/v1/issues/ENG-1/activity", ss.secrets[apitokens.ScopeOperator])
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "no_activity")
	act.err = errors.New("disk on fire")
	rec = doReq(t, ss.srv, http.MethodGet, "/api/v1/issues/ENG-1/activity", ss.secrets[apitokens.ScopeOperator])
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "disk on fire")

	none := activityServer(t, nil)
	rec = doReq(t, none.srv, http.MethodGet, "/api/v1/issues/ENG-1/activity", none.secrets[apitokens.ScopeAdmin])
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestResourceArgsHiddenBelowOperator: viewers see each top child
// command's name but not its arguments, in the snapshot and in the run
// context; operators see both.
func TestResourceArgsHiddenBelowOperator(t *testing.T) {
	ss := activityServer(t, &fakeActivity{})
	for _, path := range []string{"/api/v1/state", "/api/v1/issues/ENG-1/context"} {
		rec := doReq(t, ss.srv, http.MethodGet, path, ss.secrets[apitokens.ScopeViewer])
		require.Equal(t, http.StatusOK, rec.Code, path)
		assert.Contains(t, rec.Body.String(), `"name":"git"`, path)
		assert.NotContains(t, rec.Body.String(), "push origin", path)

		rec = doReq(t, ss.srv, http.MethodGet, path, ss.secrets[apitokens.ScopeOperator])
		require.Equal(t, http.StatusOK, rec.Code, path)
		assert.Contains(t, rec.Body.String(), "push origin feature/x", path)
	}
}
