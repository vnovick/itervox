package jira_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
)

func TestProbeProjectAccessReturnsAccount(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		switch r.Path {
		case "/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"accountId":"abc","displayName":"Ada Lovelace"}`))
		case "/rest/api/3/project/PROJ":
			_, _ = w.Write([]byte(`{"key":"PROJ"}`))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	})
	account, err := c.ProbeProjectAccess(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "Ada Lovelace", account)
	reqs := seen()
	require.Len(t, reqs, 2)
	assert.Equal(t, "GET /rest/api/3/myself", reqs[0].Method+" "+reqs[0].Path)
	assert.Equal(t, "GET /rest/api/3/project/PROJ", reqs[1].Method+" "+reqs[1].Path)
}

func TestProbeProjectAccessReportsBadToken(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.ProbeProjectAccess(t.Context())
	var apiErr *tracker.APIStatusError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 401, apiErr.Status)
	assert.Contains(t, err.Error(), "jira_probe: ")
	assert.Len(t, seen(), 1, "no project request after a failed myself")
}

func TestProbeProjectAccessReportsMissingProject(t *testing.T) {
	c, _ := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		if r.Path == "/rest/api/3/myself" {
			_, _ = w.Write([]byte(`{"accountId":"abc"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.ProbeProjectAccess(t.Context())
	require.Error(t, err)
	assert.True(t, errors.Is(err, tracker.ErrNotFound))
}
