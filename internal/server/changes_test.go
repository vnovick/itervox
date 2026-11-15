package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// fakeChanges is a ChangesClient over fixed data.
type fakeChanges struct {
	run      *RunContextRow
	diff     DiffResponse
	diffErr  error
	patch    string
	handoffs []HandoffRow
	files    []string // FileDiff calls
	port     int
}

func (f *fakeChanges) RunContext(string) (RunContextRow, bool) {
	if f.run == nil {
		return RunContextRow{}, false
	}
	return *f.run, true
}

func (f *fakeChanges) Diff(context.Context, string, string) (DiffResponse, error) {
	return f.diff, f.diffErr
}

func (f *fakeChanges) FileDiff(_ context.Context, _, _, file string) (FileDiffResponse, error) {
	f.files = append(f.files, file)
	if f.diffErr != nil {
		return FileDiffResponse{}, f.diffErr
	}
	if file != "a.go" {
		return FileDiffResponse{}, ErrDiffFileNotChanged
	}
	return FileDiffResponse{File: DiffFileRow{Path: "a.go", Status: "modified", Additions: 1}, Patch: f.patch, Bytes: len(f.patch)}, nil
}

func (f *fakeChanges) Handoffs(string) []HandoffRow { return f.handoffs }

func (f *fakeChanges) WorkspacePort(string) int { return f.port }

func newChangesServer(t *testing.T, ch ChangesClient) (*Server, map[apitokens.Scope]string) {
	t.Helper()
	store := apitokens.Open(filepath.Join(t.TempDir(), ".itervox", apitokens.FileName))
	secrets := make(map[apitokens.Scope]string)
	for _, sc := range apitokens.Scopes {
		_, secret, err := store.Create(string(sc)+"-token", sc, nil, "test")
		require.NoError(t, err)
		secrets[sc] = secret
	}
	srv := New(Config{
		Snapshot:    func() StateSnapshot { return StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
		APIToken:    scopesTestBootstrap,
		Tokens:      store,
		Changes:     ch,
	})
	return srv, secrets
}

// TestDiffEndpointScopesHunksToOperator (#215): every scope reads the file
// list; a file's hunks (?file=) need operator and are redacted below
// admin; errors map to their statuses.
func TestDiffEndpointScopesHunksToOperator(t *testing.T) {
	secret := "ghp_" + strings.Repeat("Q", 36)
	ch := &fakeChanges{
		diff:  DiffResponse{Base: "main", MergeBase: "abc", Files: []DiffFileRow{{Path: "a.go", Status: "modified", Additions: 1}}},
		patch: "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+token := \"" + secret + "\"\n",
	}
	srv, secrets := newChangesServer(t, ch)

	for _, sc := range apitokens.Scopes {
		rec := doReq(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/diff", secrets[sc])
		require.Equal(t, http.StatusOK, rec.Code, sc)
		var d DiffResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
		assert.Equal(t, "ENG-1", d.Identifier)
		assert.Equal(t, "a.go", d.Files[0].Path)
		assert.Equal(t, sc != apitokens.ScopeViewer, d.HunksAllowed, sc)
	}

	rec := doReq(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/diff?file=a.go", secrets[apitokens.ScopeViewer])
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "operator", rec.Header().Get(RequiredScopeHeader))
	assert.Empty(t, ch.files, "a viewer's hunk request never reaches git")

	rec = doReq(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/diff?file=a.go", secrets[apitokens.ScopeOperator])
	require.Equal(t, http.StatusOK, rec.Code)
	var fd FileDiffResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fd))
	assert.NotContains(t, fd.Patch, secret, "operator hunks are redacted")
	assert.Contains(t, fd.Patch, "@@ -1 +1 @@\n-x\n+token := ", "redaction keeps the diff's lines")

	rec = doReq(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/diff?file=a.go", secrets[apitokens.ScopeAdmin])
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fd))
	assert.Contains(t, fd.Patch, secret, "admins see hunks as written")

	rec = doReq(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/diff?file=other.go", secrets[apitokens.ScopeOperator])
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "file_not_changed")

	for err, want := range map[error]int{
		ErrNoWorktree:            http.StatusNotFound,
		ErrDiffBaseNotFound:      http.StatusNotFound,
		ErrBadDiffBase:           http.StatusBadRequest,
		context.DeadlineExceeded: http.StatusGatewayTimeout,
	} {
		ch.diffErr = err
		rec = doReq(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/diff", secrets[apitokens.ScopeViewer])
		assert.Equal(t, want, rec.Code, err.Error())
	}
}

// TestRunContextEndpoint (#215): the context joins the run, the worktree's
// changed files and the handoffs; an issue with no run and no worktree is
// 404; a daemon without the client is 503.
func TestRunContextEndpoint(t *testing.T) {
	ch := &fakeChanges{
		run: &RunContextRow{RunID: "run-1", Live: true, Profile: "coder", CurrentStep: "Editing",
			LastToolCall: &RunToolCall{Name: "Edit", Target: "a.go"}, Steers: []RunSteerRow{}},
		diff:     DiffResponse{Files: []DiffFileRow{{Path: "a.go", Status: "modified"}}},
		handoffs: []HandoffRow{{Title: "Plan", Path: ".itervox/handoff/x.md"}},
		port:     41003,
	}
	srv, secrets := newChangesServer(t, ch)
	rec := doReq(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/context", secrets[apitokens.ScopeViewer])
	require.Equal(t, http.StatusOK, rec.Code)
	var resp RunContextResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Run)
	assert.Equal(t, "Edit", resp.Run.LastToolCall.Name)
	assert.Equal(t, "a.go", resp.ChangedFiles[0].Path)
	assert.Equal(t, "Plan", resp.Handoffs[0].Title)
	assert.Equal(t, 41003, resp.Port, "#250: the workspace's ITERVOX_PORT")

	ch.diffErr = ErrDiffBaseNotFound
	rec = doReq(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/context", secrets[apitokens.ScopeViewer])
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "base_not_found", resp.ChangedFilesError)

	ch.run, ch.handoffs, ch.diffErr = nil, nil, ErrNoWorktree
	rec = doReq(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/context", secrets[apitokens.ScopeViewer])
	assert.Equal(t, http.StatusNotFound, rec.Code)

	bare, secrets2 := newChangesServer(t, nil)
	rec = doReq(t, bare, http.MethodGet, "/api/v1/issues/ENG-1/context", secrets2[apitokens.ScopeViewer])
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	rec = doReq(t, bare, http.MethodGet, "/api/v1/issues/ENG-1/diff", secrets2[apitokens.ScopeViewer])
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
