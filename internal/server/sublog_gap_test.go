package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/server"
)

// CORE-027 (server side of the sublog stream): a Last-Event-ID past the
// current entries — the session files were cleared or replaced — used to reset
// the cursor to 0 and replay silently, so a client that keeps its lines across
// reconnects could not tell the replay from new lines. The reset is now
// announced with one "event: gap" frame stamped id 0 (the seq before the
// replay), exactly like the issue-log stream's gap (CORE-003).
func TestHandleSubLogStream_StaleLastEventIDSendsGapBeforeReplay(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	ctx, cancel := context.WithCancel(context.Background())
	cfg.Client = &server.FuncClient{
		FetchSubLogsFn: func(context.Context, string) ([]domain.IssueLogEntry, error) {
			defer cancel()
			return []domain.IssueLogEntry{{Event: "text", Message: "first"}, {Event: "text", Message: "second"}}, nil
		},
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/sublog-stream", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "999")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	body := w.Body.String()
	// CORE-153: ids are "<epoch>-<seq>"; a legacy bare id is a foreign epoch.
	epoch, _ := lastSublogID(t, body)
	gapAt := strings.Index(body, "id: "+epoch+"-0\nevent: gap\n")
	require.GreaterOrEqual(t, gapAt, 0, "a stale cursor must be announced with a gap frame; body:\n%s", body)
	firstAt := strings.Index(body, `"message":"first"`)
	require.Greater(t, firstAt, gapAt, "the gap frame must precede the replay")
	assert.Contains(t, body, "id: "+epoch+"-1\nevent: sublog")
	assert.Contains(t, body, "id: "+epoch+"-2\nevent: sublog")
	assert.Equal(t, 1, strings.Count(body, "event: gap"))
}

// A caught-up or in-range cursor never gets a gap.
func TestHandleSubLogStream_InRangeCursorHasNoGap(t *testing.T) {
	entries := entriesOf("first", "second")
	epoch, _ := lastSublogID(t, sublogOnce(t, entries, ""))
	for _, last := range []string{"", epoch + "-1", epoch + "-2"} {
		assert.NotContains(t, sublogOnce(t, entries, last), "event: gap", "Last-Event-ID %q", last)
	}
}
