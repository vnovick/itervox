package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// fakeAttestations is an AttestationsClient over fixed data.
type fakeAttestations struct {
	asked []string
	err   error
}

func (f *fakeAttestations) Attestations(identifier string) (AttestationsResponse, error) {
	f.asked = append(f.asked, identifier)
	if f.err != nil {
		return AttestationsResponse{}, f.err
	}
	if identifier != "ENG-1" {
		return AttestationsResponse{}, nil
	}
	return AttestationsResponse{KeyID: "0123456789abcdef", Runs: []AttestationRunView{{
		RunID: "2026-11-14T10-00-00.000Z_implementer", Profile: "implementer", FinalTree: "aaa",
		Records: []AttestationRecordView{
			{Kind: "check", Name: "test", Command: "make test", ExitCode: 1, TreeHash: "aaa", Verified: true},
			{Kind: "seal", TreeHash: "aaa", Verified: true},
		},
	}}}, nil
}

// TestIssueAttestationsEndpoint (signed checks): a viewer token reads an
// issue's records with their verified flag; an issue with none is an empty
// list, not null; a read error is a redacted 500; no client is a 503.
func TestIssueAttestationsEndpoint(t *testing.T) {
	store := apitokens.Open(filepath.Join(t.TempDir(), ".itervox", apitokens.FileName))
	_, viewer, err := store.Create("viewer-token", apitokens.ScopeViewer, nil, "test")
	require.NoError(t, err)
	fa := &fakeAttestations{}
	srv := New(Config{
		Snapshot: func() StateSnapshot { return StateSnapshot{} }, RefreshChan: make(chan struct{}, 1),
		APIToken: scopesTestBootstrap, Tokens: store, Attestations: fa,
	})

	rec := doBody(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/attestations", viewer, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp AttestationsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "ENG-1", resp.Identifier)
	require.Len(t, resp.Runs, 1)
	require.Len(t, resp.Runs[0].Records, 2)
	assert.Equal(t, 1, resp.Runs[0].Records[0].ExitCode)
	assert.True(t, resp.Runs[0].Records[0].Verified)
	assert.Contains(t, rec.Body.String(), `"verified":true`)
	assert.Contains(t, rec.Body.String(), `"exitCode":1`)

	rec = doBody(t, srv, http.MethodGet, "/api/v1/issues/ENG-2/attestations", viewer, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"identifier":"ENG-2","runs":[]}`, rec.Body.String())

	rec = doBody(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/attestations", "", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the route is behind auth")

	secret := "ghp_" + strings.Repeat("Z", 36)
	fa.err = errors.New("open " + secret + ": permission denied")
	rec = doBody(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/attestations", viewer, "")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), secret)

	bare := New(Config{
		Snapshot: func() StateSnapshot { return StateSnapshot{} }, RefreshChan: make(chan struct{}, 1),
		APIToken: scopesTestBootstrap, Tokens: store,
	})
	rec = doBody(t, bare, http.MethodGet, "/api/v1/issues/ENG-1/attestations", viewer, "")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
