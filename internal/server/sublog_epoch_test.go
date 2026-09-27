package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/server"
)

// sublogOnce serves one sublog-stream request over entries (the stream ends
// after the initial send) and returns the SSE body.
func sublogOnce(t *testing.T, entries []domain.IssueLogEntry, lastEventID string) string {
	t.Helper()
	cfg := makeTestConfig(baseSnap())
	ctx, cancel := context.WithCancel(context.Background())
	cfg.Client = &server.FuncClient{
		FetchSubLogsFn: func(context.Context, string) ([]domain.IssueLogEntry, error) {
			defer cancel()
			return entries, nil
		},
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/sublog-stream", nil).WithContext(ctx)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w.Body.String()
}

var sublogIDRe = regexp.MustCompile(`id: (\d+)-(\d+)\nevent: sublog`)

func lastSublogID(t *testing.T, body string) (epoch, id string) {
	t.Helper()
	m := sublogIDRe.FindAllStringSubmatch(body, -1)
	require.NotEmpty(t, m, "no sublog frames with <epoch>-<seq> ids; body:\n%s", body)
	last := m[len(m)-1]
	return last[1], last[1] + "-" + last[2]
}

// entriesOf builds session-log entries; the session id is the message's
// first letter ("a1", "a2" → session "sess-a"), as a set of entries parsed
// from one session file would carry it.
func entriesOf(msgs ...string) []domain.IssueLogEntry {
	out := make([]domain.IssueLogEntry, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, domain.IssueLogEntry{Event: "text", Message: m, SessionID: "sess-" + m[:1]})
	}
	return out
}

// TestHandleSubLogStream_StaleCursorInsideLongerReplacementGetsGap
// (CORE-153): a cursor from an earlier set of session files that falls
// INSIDE a longer replacement set used to resume mid-way into different
// content with no gap. Ids now carry the set's epoch, so the replacement is
// announced with a gap and replayed from the start.
func TestHandleSubLogStream_StaleCursorInsideLongerReplacementGetsGap(t *testing.T) {
	oldEpoch, cursor := lastSublogID(t, sublogOnce(t, entriesOf("a1", "a2"), ""))
	require.True(t, strings.HasSuffix(cursor, "-2"))

	body := sublogOnce(t, entriesOf("b1", "b2", "b3", "b4", "b5"), cursor)
	newEpoch, last := lastSublogID(t, body)
	require.NotEqual(t, oldEpoch, newEpoch, "a replacement set gets a new epoch")
	gapAt := strings.Index(body, "id: "+newEpoch+"-0\nevent: gap\n")
	require.GreaterOrEqual(t, gapAt, 0, "the replacement must be announced with a gap; body:\n%s", body)
	firstAt := strings.Index(body, `"message":"b1"`)
	require.Greater(t, firstAt, gapAt, "the full replacement is replayed after the gap")
	assert.Equal(t, newEpoch+"-5", last)
}

// TestHandleSubLogStream_AppendedSetResumesWithoutGap (CORE-153): the same
// set with lines appended keeps its epoch and resumes after the cursor.
func TestHandleSubLogStream_AppendedSetResumesWithoutGap(t *testing.T) {
	epoch, cursor := lastSublogID(t, sublogOnce(t, entriesOf("a1", "a2"), ""))
	body := sublogOnce(t, entriesOf("a1", "a2", "a3"), cursor)
	assert.NotContains(t, body, "event: gap")
	assert.NotContains(t, body, `"message":"a1"`, "already-seen lines are not replayed")
	assert.Contains(t, body, "id: "+epoch+"-3\nevent: sublog")
}
