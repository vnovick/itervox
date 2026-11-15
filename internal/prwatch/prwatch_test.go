package prwatch

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const prURL = "https://github.com/acme/app/pull/7"

// fakeAPI serves canned GitHub responses by path and records the paths hit.
type fakeAPI struct {
	mu      sync.Mutex
	routes  map[string]string
	hits    []string
	headers http.Header
	n304    atomic.Int64
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits = append(f.hits, r.URL.Path)
	body, ok := f.routes[r.URL.Path]
	f.headers = r.Header.Clone()
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256([]byte(body))
	etag := `"` + hex.EncodeToString(sum[:8]) + `"` // changes with the body, like GitHub's
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", "4321")
	if r.Header.Get("If-None-Match") == etag {
		f.n304.Add(1)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	_, _ = w.Write([]byte(body))
}

func (f *fakeAPI) set(path, body string) {
	f.mu.Lock()
	f.routes[path] = body
	f.mu.Unlock()
}

func (f *fakeAPI) hitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.hits)
}

type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) emit(_ context.Context, ev Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) take() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func openPR(sha, mergeable string) string {
	return `{"state":"open","number":7,"html_url":"` + prURL + `","mergeable_state":"` + mergeable + `","head":{"sha":"` + sha + `","ref":"feat"}}`
}

func newTestWatcher(t *testing.T, kinds map[string]bool, webhooks bool, since time.Time) (*Watcher, *fakeAPI, *recorder) {
	t.Helper()
	api := &fakeAPI{routes: map[string]string{}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	rec := &recorder{}
	w := New(Config{
		Client: &Client{HTTP: srv.Client(), Token: func(string) string { return "tok" },
			BaseURL: func(string) string { return srv.URL }},
		Managed:  func() []Managed { return []Managed{{URL: prURL, Identifier: "ENG-7", Since: since}} },
		Kinds:    func() map[string]bool { return kinds },
		Emit:     rec.emit,
		Webhooks: webhooks,
	})
	return w, api, rec
}

func TestParsePRURL(t *testing.T) {
	ref, ok := ParsePRURL("https://GitHub.com/acme/app/pull/7")
	require.True(t, ok)
	assert.Equal(t, PRRef{Host: "github.com", Owner: "acme", Repo: "app", Number: 7}, ref)
	_, ok = ParsePRURL("https://github.com/acme/app/issues/7")
	assert.False(t, ok)
	_, ok = ParsePRURL("https://github.com/acme/app/pull/0")
	assert.False(t, ok)
}

// TestPollEmitsChecksStatusesAndReviewsOnce (#173): the poll emits each
// completed check run, the current state of each status context and each
// submitted review once, skipping what predates the PR's management,
// pending reviews and runs still in progress; a repeated poll costs 304s
// and emits nothing.
func TestPollEmitsChecksStatusesAndReviewsOnce(t *testing.T) {
	since := time.Now().Add(-time.Hour)
	old := since.Add(-time.Minute).UTC().Format(time.RFC3339)
	recent := time.Now().UTC().Format(time.RFC3339)
	w, api, rec := newTestWatcher(t, map[string]bool{CheckCompleted: true, ReviewSubmitted: true}, false, since)
	api.set("/repos/acme/app/pulls/7", openPR("aaa", "clean"))
	api.set("/repos/acme/app/commits/aaa/check-runs", `{"check_runs":[
		{"name":"ci","status":"completed","conclusion":"FAILURE","completed_at":"`+recent+`"},
		{"name":"old","status":"completed","conclusion":"failure","completed_at":"`+old+`"},
		{"name":"slow","status":"in_progress"}]}`)
	api.set("/repos/acme/app/commits/aaa/statuses", `[
		{"context":"chromatic","state":"failure","updated_at":"`+recent+`"},
		{"context":"chromatic","state":"success","updated_at":"`+recent+`"},
		{"context":"deploy","state":"pending","updated_at":"`+recent+`"}]`)
	api.set("/repos/acme/app/pulls/7/reviews", `[
		{"id":1,"state":"COMMENTED","submitted_at":"`+old+`","user":{"login":"bot"}},
		{"id":2,"state":"PENDING","user":{"login":"bot"}},
		{"id":3,"state":"CHANGES_REQUESTED","submitted_at":"`+recent+`","user":{"login":"greptile-apps[bot]"}}]`)

	w.Poll(t.Context())
	got := rec.take()
	require.Len(t, got, 3, "%+v", got)
	assert.Equal(t, Event{Kind: CheckCompleted, Identifier: "ENG-7", PRURL: prURL, PRNumber: 7, Branch: "feat",
		HeadSHA: "aaa", MergeableState: "clean", CheckName: "ci", Conclusion: "failure",
		CompletedAt: got[0].CompletedAt, Source: SourcePoll}, got[0])
	assert.Equal(t, "chromatic", got[1].CheckName)
	assert.Equal(t, "failure", got[1].Conclusion, "the newest status of a context is its state")
	assert.Equal(t, ReviewSubmitted, got[2].Kind)
	assert.Equal(t, "3", got[2].ReviewID)
	assert.Equal(t, "changes_requested", got[2].ReviewState)
	assert.Equal(t, "greptile-apps[bot]", got[2].ReviewAuthor)
	assert.Equal(t, "Bearer tok", api.headers.Get("Authorization"))
	assert.Equal(t, 4321, w.cfg.Client.RateLimitSnapshot().RequestsRemaining)

	before := api.hitCount()
	w.Poll(t.Context())
	assert.Empty(t, rec.take())
	assert.Equal(t, int64(api.hitCount()-before), api.n304.Load(), "a second poll is conditional only")
}

// TestPollSynchronizedAndMergeable (#173): a new head and a new
// mergeable_state are emitted once each; "unknown" is never emitted.
func TestPollSynchronizedAndMergeable(t *testing.T) {
	w, api, rec := newTestWatcher(t, map[string]bool{Synchronized: true, MergeableChanged: true}, false, time.Now())
	api.set("/repos/acme/app/pulls/7", openPR("aaa", "unknown"))
	w.Poll(t.Context())
	got := rec.take()
	require.Len(t, got, 1)
	assert.Equal(t, Synchronized, got[0].Kind)
	w.Poll(t.Context())
	assert.Empty(t, rec.take())
	api.set("/repos/acme/app/pulls/7", openPR("bbb", "dirty"))
	w.Poll(t.Context())
	got = rec.take()
	require.Len(t, got, 2)
	assert.Equal(t, "bbb", got[0].HeadSHA)
	assert.Equal(t, MergeableChanged, got[1].Kind)
	assert.Equal(t, "dirty", got[1].MergeableState)
}

// TestPollStopsAtClosedPR (#173): a closed PR is read once and then left
// alone; no PR-event rule means no request at all.
func TestPollStopsAtClosedPR(t *testing.T) {
	w, api, rec := newTestWatcher(t, map[string]bool{CheckCompleted: true}, false, time.Now())
	api.set("/repos/acme/app/pulls/7", `{"state":"closed","merged":true,"number":7,"head":{"sha":"aaa"}}`)
	w.Poll(t.Context())
	w.Poll(t.Context())
	assert.Equal(t, 1, api.hitCount())
	assert.Empty(t, rec.take())

	idle, idleAPI, _ := newTestWatcher(t, map[string]bool{}, false, time.Now())
	idle.Poll(t.Context())
	assert.Zero(t, idleAPI.hitCount())
}

// TestPollWithWebhooksReadsOnlyThePR (#173): with webhooks delivering checks
// and reviews, the poll reads only the PR itself.
func TestPollWithWebhooksReadsOnlyThePR(t *testing.T) {
	w, api, _ := newTestWatcher(t, map[string]bool{CheckCompleted: true, ReviewSubmitted: true, MergeableChanged: true}, true, time.Now())
	api.set("/repos/acme/app/pulls/7", openPR("aaa", "clean"))
	w.Poll(t.Context())
	assert.Equal(t, []string{"/repos/acme/app/pulls/7"}, api.hits)
}

// TestClientFollowsNextPageOnlyOnAPIRoot: a Link header pointing elsewhere is
// not followed (the token would go with it).
func TestClientFollowsNextPageOnlyOnAPIRoot(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"id":2,"state":"APPROVED","submitted_at":"2026-01-01T00:00:00Z","user":{"login":"b"}}]`))
			return
		}
		w.Header().Set("Link", `<`+srvURL+`/repos/acme/app/pulls/7/reviews?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[{"id":1,"state":"COMMENTED","submitted_at":"2026-01-01T00:00:00Z","user":{"login":"a"}}]`))
	}))
	t.Cleanup(srv.Close)
	srvURL = srv.URL
	c := &Client{HTTP: srv.Client(), BaseURL: func(string) string { return srv.URL }}
	reviews, err := c.Reviews(t.Context(), PRRef{Host: "github.com", Owner: "acme", Repo: "app", Number: 7})
	require.NoError(t, err)
	assert.Len(t, reviews, 2)

	other := &Client{HTTP: srv.Client(), BaseURL: func(string) string { return srv.URL + "/api" }}
	_, err = other.get(t.Context(), "github.com", srv.URL+"/repos/acme/app/pulls/7/reviews", &[]Review{})
	require.NoError(t, err)
	next, err := other.get(t.Context(), "github.com", srv.URL+"/repos/acme/app/pulls/7/reviews", &[]Review{})
	require.NoError(t, err)
	assert.Empty(t, next, "a next page off the API root is not followed")
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func post(h *Handler, event, sig string, body []byte) int {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/pr-events", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", sig)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func drain(h *Handler) []Event {
	var out []Event
	for {
		select {
		case ev := <-h.queue:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// TestWebhookDeliveries (#173): signed deliveries for a managed PR become
// events; a bad signature is refused; an unmanaged PR, a pending status and
// other actions yield nothing; a status finds its PR by head SHA.
func TestWebhookDeliveries(t *testing.T) {
	const secret = "0123456789abcdef0123"
	w, _, _ := newTestWatcher(t, map[string]bool{Synchronized: true, CheckCompleted: true, ReviewSubmitted: true}, true, time.Now().Add(-time.Hour))
	h, err := NewHandler(secret, w)
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339)

	check := []byte(`{"action":"completed","check_run":{"name":"ci","head_sha":"aaa","status":"completed","conclusion":"failure",
		"completed_at":"` + now + `","pull_requests":[{"number":7,"head":{"sha":"aaa","ref":"feat"}}]},
		"repository":{"html_url":"https://github.com/acme/app"}}`)
	assert.Equal(t, http.StatusUnauthorized, post(h, "check_run", "sha256=00", check))
	assert.Equal(t, http.StatusUnauthorized, post(h, "check_run", sign("wrong-secret-0000", check), check))
	assert.Empty(t, drain(h))

	assert.Equal(t, http.StatusAccepted, post(h, "check_run", sign(secret, check), check))
	got := drain(h)
	require.Len(t, got, 1)
	assert.Equal(t, Event{Kind: CheckCompleted, Identifier: "ENG-7", PRURL: prURL, PRNumber: 7, Branch: "feat", HeadSHA: "aaa",
		CheckName: "ci", Conclusion: "failure", CompletedAt: got[0].CompletedAt, Source: SourceWebhook}, got[0])
	assert.Equal(t, http.StatusAccepted, post(h, "check_run", sign(secret, check), check))
	assert.Empty(t, drain(h), "a re-delivery emits nothing")

	unmanaged := []byte(strings.ReplaceAll(string(check), `"number":7`, `"number":8`))
	assert.Equal(t, http.StatusAccepted, post(h, "check_run", sign(secret, unmanaged), unmanaged))
	assert.Empty(t, drain(h), "an unmanaged PR yields nothing")

	status := []byte(`{"sha":"aaa","context":"chromatic","state":"failure","updated_at":"` + now + `"}`)
	pending := []byte(`{"sha":"aaa","context":"chromatic","state":"pending","updated_at":"` + now + `"}`)
	assert.Equal(t, http.StatusAccepted, post(h, "status", sign(secret, pending), pending))
	assert.Empty(t, drain(h))
	assert.Equal(t, http.StatusAccepted, post(h, "status", sign(secret, status), status))
	got = drain(h)
	require.Len(t, got, 1, "the status is routed to the PR whose head is aaa")
	assert.Equal(t, "chromatic", got[0].CheckName)

	sync := []byte(`{"action":"synchronize","pull_request":{"html_url":"` + prURL + `","number":7,"head":{"sha":"bbb","ref":"feat"}}}`)
	assert.Equal(t, http.StatusAccepted, post(h, "pull_request", sign(secret, sync), sync))
	assert.Equal(t, http.StatusAccepted, post(h, "pull_request", sign(secret, sync), sync))
	got = drain(h)
	require.Len(t, got, 1, "one Synchronized per head")
	assert.Equal(t, Synchronized, got[0].Kind)
	assert.Equal(t, "bbb", got[0].HeadSHA)

	review := []byte(`{"action":"submitted","review":{"id":99,"state":"commented","submitted_at":"` + now + `",
		"user":{"login":"greptile-apps[bot]"}},"pull_request":{"html_url":"` + prURL + `","number":7,"head":{"sha":"bbb","ref":"feat"}}}`)
	assert.Equal(t, http.StatusAccepted, post(h, "pull_request_review", sign(secret, review), review))
	got = drain(h)
	require.Len(t, got, 1)
	assert.Equal(t, ReviewSubmitted, got[0].Kind)
	assert.Equal(t, "99", got[0].ReviewID)
	assert.Equal(t, "bbb", got[0].HeadSHA)

	ping := []byte(`{"zen":"x"}`)
	assert.Equal(t, http.StatusAccepted, post(h, "ping", sign(secret, ping), ping))
	bad := []byte(`{`)
	assert.Equal(t, http.StatusBadRequest, post(h, "check_run", sign(secret, bad), bad))
}
