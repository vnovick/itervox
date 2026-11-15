package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/metrics"
)

type fakeStore struct {
	mu        sync.Mutex
	refreshed []string
	forgotten []string
	syncs     int
	known     map[string]bool
}

func (f *fakeStore) RefreshIssues(_ context.Context, ids []string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var unknown []string
	for _, id := range ids {
		if f.known != nil && !f.known[id] {
			unknown = append(unknown, id)
			continue
		}
		f.refreshed = append(f.refreshed, id)
	}
	return unknown, nil
}

func (f *fakeStore) Forget(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, id)
}

func (f *fakeStore) RequestSync() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncs++
}

func (f *fakeStore) state() ([]string, []string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.refreshed), slices.Clone(f.forgotten), f.syncs
}

const secret = "whsec_unit"

func sign(body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func newHandler(t *testing.T, tracker string, st *fakeStore) *Handler {
	t.Helper()
	h, err := New(Config{Tracker: tracker, Secret: secret, Repo: "owner/repo", Store: st, Now: func() time.Time { return now }})
	require.NoError(t, err)
	return h
}

func post(h http.Handler, headers map[string]string, body string) int {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/x", bytes.NewBufferString(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func linearBody(action, typ, id string, at time.Time) string {
	return fmt.Sprintf(`{"action":%q,"type":%q,"data":{"id":%q},"webhookTimestamp":%d}`, action, typ, id, at.UnixMilli())
}

func githubBody(action string, number int, repo string, pr bool) string {
	prField := ""
	if pr {
		prField = `,"pull_request":{"url":"x"}`
	}
	return fmt.Sprintf(`{"action":%q,"issue":{"number":%d%s},"repository":{"full_name":%q}}`, action, number, prField, repo)
}

// TestWebhookRejectsAnInvalidSignature: a missing, malformed or wrong
// signature (or one made with another secret) is a 401, counted, and
// nothing is queued.
func TestWebhookRejectsAnInvalidSignature(t *testing.T) {
	for _, tracker := range []string{"linear", "github"} {
		st := &fakeStore{}
		h := newHandler(t, tracker, st)
		header := "Linear-Signature"
		prefix := ""
		body := linearBody("update", "Issue", "abc", now)
		if tracker == "github" {
			header, prefix = "X-Hub-Signature-256", "sha256="
			body = githubBody("edited", 7, "owner/repo", false)
		}
		other := hmac.New(sha256.New, []byte("another secret"))
		other.Write([]byte(body))
		before := metrics.WebhookCount(tracker, metrics.WebhookBadSignature)
		for name, sig := range map[string]string{
			"missing":      "",
			"not hex":      prefix + "zz",
			"wrong secret": prefix + hex.EncodeToString(other.Sum(nil)),
			"other body":   prefix + sign(body+" "),
		} {
			headers := map[string]string{"X-GitHub-Event": "issues"}
			if sig != "" {
				headers[header] = sig
			}
			assert.Equal(t, http.StatusUnauthorized, post(h, headers, body), "%s %s", tracker, name)
		}
		assert.Equal(t, before+4, metrics.WebhookCount(tracker, metrics.WebhookBadSignature))
		h.drain(t.Context())
		refreshed, forgotten, syncs := st.state()
		assert.Empty(t, refreshed, tracker)
		assert.Empty(t, forgotten, tracker)
		assert.Zero(t, syncs, tracker)

		// The same body correctly signed is accepted.
		headers := map[string]string{"X-GitHub-Event": "issues", header: prefix + sign(body)}
		assert.Equal(t, http.StatusAccepted, post(h, headers, body), tracker)
	}
}

func TestLinearDeliveryOutsideTheReplayWindowIsRejected(t *testing.T) {
	st := &fakeStore{}
	h := newHandler(t, "linear", st)
	before := metrics.WebhookCount("linear", metrics.WebhookStale)
	for _, at := range []time.Time{now.Add(-2 * time.Minute), now.Add(2 * time.Minute), time.UnixMilli(0)} {
		body := linearBody("update", "Issue", "abc", at)
		assert.Equal(t, http.StatusUnauthorized, post(h, map[string]string{"Linear-Signature": sign(body)}, body))
	}
	body := `{"action":"update","type":"Issue","data":{"id":"abc"}}`
	assert.Equal(t, http.StatusUnauthorized, post(h, map[string]string{"Linear-Signature": sign(body)}, body), "no timestamp")
	assert.Equal(t, before+4, metrics.WebhookCount("linear", metrics.WebhookStale))
}

func TestLinearDeliveries(t *testing.T) {
	st := &fakeStore{known: map[string]bool{"a": true, "b": true}}
	h := newHandler(t, "linear", st)
	send := func(body string) int { return post(h, map[string]string{"Linear-Signature": sign(body)}, body) }

	assert.Equal(t, http.StatusAccepted, send(linearBody("update", "Issue", "a", now)))
	assert.Equal(t, http.StatusAccepted, send(linearBody("update", "Issue", "a", now)), "a repeat coalesces")
	assert.Equal(t, http.StatusAccepted, send(linearBody("remove", "Issue", "b", now)))
	assert.Equal(t, http.StatusAccepted, send(linearBody("create", "Issue", "new", now)))
	ignored := metrics.WebhookCount("linear", metrics.WebhookIgnored)
	assert.Equal(t, http.StatusAccepted, send(linearBody("create", "Comment", "c1", now)))
	assert.Equal(t, ignored+1, metrics.WebhookCount("linear", metrics.WebhookIgnored), "a comment names nothing to refresh")
	assert.Equal(t, http.StatusBadRequest, send(`{"type":`), "malformed")

	h.drain(t.Context())
	refreshed, forgotten, syncs := st.state()
	assert.Equal(t, []string{"a"}, refreshed)
	assert.Equal(t, []string{"b"}, forgotten)
	assert.Equal(t, 1, syncs, "the unknown issue asks for one pull")
}

func TestGitHubDeliveries(t *testing.T) {
	st := &fakeStore{}
	h := newHandler(t, "github", st)
	send := func(event, body string) int {
		return post(h, map[string]string{"X-GitHub-Event": event, "X-Hub-Signature-256": "sha256=" + sign(body)}, body)
	}
	assert.Equal(t, http.StatusAccepted, send("issues", githubBody("labeled", 7, "Owner/Repo", false)), "repository match ignores case")
	assert.Equal(t, http.StatusAccepted, send("issues", githubBody("deleted", 8, "owner/repo", false)))
	assert.Equal(t, http.StatusAccepted, send("issues", githubBody("transferred", 9, "owner/repo", false)))
	assert.Equal(t, http.StatusAccepted, send("issues", githubBody("edited", 10, "someone/else", false)), "another repository is ignored")
	assert.Equal(t, http.StatusAccepted, send("issues", githubBody("edited", 11, "owner/repo", true)), "a pull request is ignored")
	assert.Equal(t, http.StatusAccepted, send("ping", `{"zen":"hi"}`))
	assert.Equal(t, http.StatusAccepted, send("issue_comment", githubBody("created", 7, "owner/repo", false)))
	assert.Equal(t, http.StatusBadRequest, send("issues", `{"action":"edited"}`))

	h.drain(t.Context())
	refreshed, forgotten, syncs := st.state()
	assert.Equal(t, []string{"7"}, refreshed)
	assert.ElementsMatch(t, []string{"8", "9"}, forgotten)
	assert.Zero(t, syncs)
}

func TestWebhookFloodFallsBackToOnePull(t *testing.T) {
	st := &fakeStore{}
	h := newHandler(t, "linear", st)
	for i := range maxPending + 10 {
		body := linearBody("update", "Issue", fmt.Sprintf("id-%d", i), now)
		require.Equal(t, http.StatusAccepted, post(h, map[string]string{"Linear-Signature": sign(body)}, body))
	}
	h.drain(t.Context())
	refreshed, _, syncs := st.state()
	assert.Less(t, len(refreshed), maxPending, "the overflowed queue was dropped")
	assert.Equal(t, 1, syncs)
}

func TestWebhookRequiresASecretAndAKnownTracker(t *testing.T) {
	_, err := New(Config{Tracker: "linear", Store: &fakeStore{}})
	assert.ErrorContains(t, err, "secret")
	_, err = New(Config{Tracker: "local", Secret: secret, Store: &fakeStore{}})
	assert.Error(t, err)

	h := newHandler(t, "linear", &fakeStore{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/webhooks/linear", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	big := bytes.Repeat([]byte("x"), MaxBody+1)
	assert.Equal(t, http.StatusRequestEntityTooLarge, post(h, nil, string(big)))
}

func TestWebhookFloodOfRemovalsIsBoundedToo(t *testing.T) {
	st := &fakeStore{}
	h := newHandler(t, "linear", st)
	for i := range maxPending + 10 {
		body := linearBody("remove", "Issue", fmt.Sprintf("id-%d", i), now)
		require.Equal(t, http.StatusAccepted, post(h, map[string]string{"Linear-Signature": sign(body)}, body))
	}
	h.mu.Lock()
	pending := len(h.refresh) + len(h.forget)
	h.mu.Unlock()
	assert.LessOrEqual(t, pending, maxPending, "the removal queue is bounded")
	h.drain(t.Context())
	_, forgotten, syncs := st.state()
	assert.Less(t, len(forgotten), maxPending)
	assert.Equal(t, 1, syncs, "the dropped removals are covered by one pull")
}

// countingHandler counts the log records at WARN and above.
type countingHandler struct {
	mu sync.Mutex
	n  int
}

func (c *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *countingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		c.mu.Lock()
		c.n++
		c.mu.Unlock()
	}
	return nil
}
func (c *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *countingHandler) WithGroup(string) slog.Handler      { return c }

// Dropped deliveries are all counted but logged at most once a minute: the
// route needs no token, so anyone who can reach it could flood the log.
func TestWebhookDropsAreLoggedAtMostOnceAMinute(t *testing.T) {
	logs := &countingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(prev) })

	clock := now
	h, err := New(Config{Tracker: "linear", Secret: secret, Store: &fakeStore{}, Now: func() time.Time { return clock }})
	require.NoError(t, err)
	before := metrics.WebhookCount("linear", metrics.WebhookBadSignature)
	for range 5 {
		assert.Equal(t, http.StatusUnauthorized, post(h, nil, linearBody("update", "Issue", "a", clock)))
	}
	assert.Equal(t, before+5, metrics.WebhookCount("linear", metrics.WebhookBadSignature))
	assert.Equal(t, 1, logs.n)
	clock = clock.Add(time.Minute)
	post(h, nil, linearBody("update", "Issue", "a", clock))
	assert.Equal(t, 2, logs.n)
}
