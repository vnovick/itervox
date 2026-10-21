package store_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/store"
	"github.com/vnovick/itervox/internal/tracker/webhook"
)

// #131 — a signed webhook delivery refreshes the store by issue ID.

const hookSecret = "whsec_test"

func linearDelivery(t *testing.T, h http.Handler, action, id string, at time.Time) int {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"action":%q,"type":"Issue","data":{"id":%q},"webhookTimestamp":%d}`, action, id, at.UnixMilli()))
	mac := hmac.New(sha256.New, []byte(hookSecret))
	mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear", bytes.NewReader(body))
	req.Header.Set("Linear-Signature", hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func hookedStore(t *testing.T, tr *countingTracker, nudgeDelay ...time.Duration) (*store.Store, *webhook.Handler) {
	t.Helper()
	cfg := storeConfig(t, t.TempDir())
	cfg.NudgeDelay = 10 * time.Millisecond
	if len(nudgeDelay) > 0 {
		cfg.NudgeDelay = nudgeDelay[0]
	}
	_, s, err := store.Wrap(tr, cfg)
	require.NoError(t, err)
	h, err := webhook.New(webhook.Config{Tracker: "linear", Secret: hookSecret, Store: s})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.Run(ctx) }()
	go func() { defer wg.Done(); h.Run(ctx) }()
	// Before the temp dir goes: both write the store file.
	t.Cleanup(func() { cancel(); wg.Wait() })
	return s, h
}

// TestWebhookDeliveryUpdatesTheStoreWithoutAListRequest: an issue changed in
// the tracker is right on the store's board within seconds of the delivery,
// by one read by ID and no list request.
func TestWebhookDeliveryUpdatesTheStoreWithoutAListRequest(t *testing.T) {
	tr := newCounting(issue("1", "Todo"), issue("2", "Todo"))
	s, h := hookedStore(t, tr)
	require.Eventually(t, func() bool {
		got, err := s.FetchCandidateIssues(t.Context())
		return err == nil && len(got) == 2
	}, 5*time.Second, 5*time.Millisecond, "the cold pull")
	lists := tr.listCalls()
	pulls := metrics.StorePullCount(metrics.StorePullWebhook)
	accepted := metrics.WebhookCount("linear", metrics.WebhookAccepted)

	// Someone moves ENG-1 to Done in the tracker.
	require.NoError(t, tr.UpdateIssueState(t.Context(), "1", "Done"))
	require.Equal(t, http.StatusAccepted, linearDelivery(t, h, "update", "1", time.Now()))

	start := time.Now()
	require.Eventually(t, func() bool {
		got, _ := s.FetchCandidateIssues(t.Context())
		return len(got) == 1 && got[0].ID == "2"
	}, 5*time.Second, 5*time.Millisecond, "the delivery takes ENG-1 off the candidates")
	assert.Less(t, time.Since(start), 2*time.Second)
	done, err := s.FetchIssuesByStates(t.Context(), board)
	require.NoError(t, err)
	assert.Contains(t, identifiers(done), "ENG-1")

	assert.Equal(t, lists, tr.listCalls(), "no list request")
	assert.Equal(t, pulls, metrics.StorePullCount(metrics.StorePullWebhook), "no pull")
	assert.Equal(t, accepted+1, metrics.WebhookCount("linear", metrics.WebhookAccepted))
}

// A delivery for an issue the store does not hold (new, or moved into scope)
// asks for one pull; a burst of them still costs one.
func TestWebhookForAnUnknownIssuePullsOnce(t *testing.T) {
	prev := store.MinNudgeGap
	store.MinNudgeGap = 0
	t.Cleanup(func() { store.MinNudgeGap = prev })
	tr := newCounting(issue("1", "Todo"))
	// The burst lands well inside the nudge delay, so it coalesces.
	const nudgeDelay = 200 * time.Millisecond
	s, h := hookedStore(t, tr, nudgeDelay)
	require.Eventually(t, func() bool {
		got, err := s.FetchCandidateIssues(t.Context())
		return err == nil && len(got) == 1
	}, 5*time.Second, 5*time.Millisecond)
	pulls := metrics.StorePullCount(metrics.StorePullWebhook)

	created, err := tr.CreateIssue(t.Context(), "", "New", "", "Todo")
	require.NoError(t, err)
	for range 5 {
		require.Equal(t, http.StatusAccepted, linearDelivery(t, h, "create", created.ID, time.Now()))
	}
	require.Eventually(t, func() bool {
		got, _ := s.FetchCandidateIssues(t.Context())
		return len(got) == 2
	}, 5*time.Second, 5*time.Millisecond, "the pull places the new issue")
	time.Sleep(3 * nudgeDelay) // long enough for a second pull to show

	assert.Equal(t, pulls+1, metrics.StorePullCount(metrics.StorePullWebhook), "the burst cost one pull")
}

func TestWebhookRemoveForgetsTheIssue(t *testing.T) {
	tr := newCounting(issue("1", "Todo"), issue("2", "Todo"))
	s, h := hookedStore(t, tr)
	require.Eventually(t, func() bool {
		got, err := s.FetchCandidateIssues(t.Context())
		return err == nil && len(got) == 2
	}, 5*time.Second, 5*time.Millisecond)
	lists := tr.listCalls()
	require.Equal(t, http.StatusAccepted, linearDelivery(t, h, "remove", "1", time.Now()))
	require.Eventually(t, func() bool {
		got, _ := s.FetchCandidateIssues(t.Context())
		return len(got) == 1 && got[0].ID == "2"
	}, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, lists, tr.listCalls())
}

// An unsigned delivery changes nothing in the store.
func TestWebhookUnsignedDeliveryLeavesTheStoreAlone(t *testing.T) {
	tr := newCounting(issue("1", "Todo"))
	s, h := hookedStore(t, tr)
	require.Eventually(t, func() bool {
		got, err := s.FetchCandidateIssues(t.Context())
		return err == nil && len(got) == 1
	}, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, tr.UpdateIssueState(t.Context(), "1", "Done"))
	body := fmt.Sprintf(`{"action":"remove","type":"Issue","data":{"id":"1"},"webhookTimestamp":%d}`, time.Now().UnixMilli())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	time.Sleep(50 * time.Millisecond)
	got, _ := s.FetchCandidateIssues(t.Context())
	assert.Len(t, got, 1, "nothing was applied")
}

// Sync requests made while Run waits out the nudge delay are covered by the
// one pull that follows.
func TestRequestSyncBurstCostsOnePull(t *testing.T) {
	prev := store.MinNudgeGap
	store.MinNudgeGap = 0
	t.Cleanup(func() { store.MinNudgeGap = prev })
	tr := newCounting(issue("1", "Todo"))
	const nudgeDelay = 200 * time.Millisecond
	s, _ := hookedStore(t, tr, nudgeDelay)
	require.Eventually(t, func() bool {
		got, err := s.FetchCandidateIssues(t.Context())
		return err == nil && len(got) == 1
	}, 5*time.Second, 5*time.Millisecond)
	pulls := metrics.StorePullCount(metrics.StorePullWebhook)

	for range 4 {
		s.RequestSync()
		time.Sleep(nudgeDelay / 5)
	}
	time.Sleep(4 * nudgeDelay)
	assert.Equal(t, pulls+1, metrics.StorePullCount(metrics.StorePullWebhook))
}

// scopedTracker is a countingTracker that can check its own scope by ID, as
// the Linear and GitHub adapters do: issues whose ID starts with "x" belong
// to another project.
type scopedTracker struct {
	*countingTracker
	scoped atomic.Int64
}

func (s *scopedTracker) FetchIssuesInScope(ctx context.Context, ids []string) ([]domain.Issue, error) {
	s.scoped.Add(1)
	var out []domain.Issue
	for _, id := range ids {
		if strings.HasPrefix(id, "x") {
			continue
		}
		if iss, err := s.FetchIssueDetail(ctx, id); err == nil && iss != nil {
			out = append(out, *iss)
		}
	}
	return out, nil
}

func scopedStore(t *testing.T, syncInterval time.Duration, issues ...domain.Issue) (*scopedTracker, *store.Store, *webhook.Handler) {
	t.Helper()
	tr := &scopedTracker{countingTracker: newCounting(issues...)}
	cfg := storeConfig(t, t.TempDir())
	cfg.SyncInterval = syncInterval
	cfg.NudgeDelay = 10 * time.Millisecond
	_, s, err := store.Wrap(tr, cfg)
	require.NoError(t, err)
	h, err := webhook.New(webhook.Config{Tracker: "linear", Secret: hookSecret, Store: s})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.Run(ctx) }()
	go func() { defer wg.Done(); h.Run(ctx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })
	require.Eventually(t, func() bool {
		_, err := s.FetchCandidateIssues(t.Context())
		return err == nil
	}, 5*time.Second, 5*time.Millisecond)
	return tr, s, h
}

// With a scope-aware adapter, a new in-scope issue is placed by ID (no list
// request), an issue of another project costs nothing, and an issue that
// left the project leaves the store.
func TestScopedWebhookPlacesNewIssuesAndDropsOutOfScopeOnes(t *testing.T) {
	tr, s, h := scopedStore(t, time.Hour, issue("1", "Todo"), issue("x9", "Todo"))
	got, _ := s.FetchCandidateIssues(t.Context())
	require.Len(t, got, 2, "the cold pull (the memory tracker has no project filter)")
	lists := tr.listCalls()

	created, err := tr.CreateIssue(t.Context(), "", "New", "", "Todo")
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, linearDelivery(t, h, "create", created.ID, time.Now()))
	require.Eventually(t, func() bool {
		got, _ := s.FetchCandidateIssues(t.Context())
		return slices.ContainsFunc(got, func(i domain.Issue) bool { return i.ID == created.ID })
	}, 5*time.Second, 5*time.Millisecond, "the new issue is placed by ID")

	require.Equal(t, http.StatusAccepted, linearDelivery(t, h, "update", "x9", time.Now()))
	require.Eventually(t, func() bool {
		got, _ := s.FetchCandidateIssues(t.Context())
		return !slices.ContainsFunc(got, func(i domain.Issue) bool { return i.ID == "x9" })
	}, 5*time.Second, 5*time.Millisecond, "an issue no longer in scope leaves the store")
	assert.Equal(t, lists, tr.listCalls(), "no list request")
}

// TestWebhooksKeepListPullsAtTheSafetyNet: under steady delivery traffic of
// every kind — issues the store holds, new in-scope issues, issues of other
// projects — the store lists only on its safety-net interval.
func TestWebhooksKeepListPullsAtTheSafetyNet(t *testing.T) {
	const safetyNet = 300 * time.Millisecond
	tr, s, h := scopedStore(t, safetyNet, issue("1", "Todo"))
	webhookPulls := metrics.StorePullCount(metrics.StorePullWebhook)
	intervalPulls := metrics.StorePullCount(metrics.StorePullInterval)
	lists := tr.listCalls()

	start := time.Now()
	for i := 0; time.Since(start) < 4*safetyNet; i++ {
		id := []string{"1", fmt.Sprintf("x%d", i)}[i%2]
		if i%10 == 0 {
			created, err := tr.CreateIssue(t.Context(), "", "New", "", "Todo")
			require.NoError(t, err)
			id = created.ID
		}
		require.Equal(t, http.StatusAccepted, linearDelivery(t, h, "update", id, time.Now()))
		time.Sleep(10 * time.Millisecond)
	}
	elapsed := time.Since(start)
	assert.Equal(t, webhookPulls, metrics.StorePullCount(metrics.StorePullWebhook), "deliveries never ask for a pull")
	pulls := metrics.StorePullCount(metrics.StorePullInterval) - intervalPulls
	assert.LessOrEqual(t, pulls, uint64(elapsed/safetyNet)+1, "pulls run at the safety-net interval")
	// One list request per configured view plus the candidates per pull.
	// A pull is counted when it starts, before its list requests: one that
	// started before the baselines above (the cold pull racing the setup's
	// first read) can still send its requests after them, so the bound
	// allows that one pull.
	assert.LessOrEqual(t, tr.listCalls()-lists, int64(pulls+1)*2)
	assert.Positive(t, tr.scoped.Load(), "deliveries were applied by ID")
	got, _ := s.FetchCandidateIssues(t.Context())
	assert.Greater(t, len(got), 1, "new issues reached the board")
}

// gatedTracker holds one full pull's candidate read, once armed, until
// released, so a local change can land while that pull is in flight.
type gatedTracker struct {
	*countingTracker
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
}

// arm makes the next candidate read wait for release; it reports entering on
// the returned channel.
func (g *gatedTracker) arm() (entered <-chan struct{}, release chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.entered, g.release = make(chan struct{}, 1), make(chan struct{})
	return g.entered, g.release
}

func (g *gatedTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	out, err := g.countingTracker.FetchCandidateIssues(ctx)
	g.mu.Lock()
	entered, release := g.entered, g.release
	g.entered, g.release = nil, nil
	g.mu.Unlock()
	if entered != nil {
		entered <- struct{}{} // buffered: never lost, never blocks
		<-release
	}
	return out, err
}

// A removal made while a pull is in flight is not undone by that pull.
func TestForgetSurvivesAConcurrentPull(t *testing.T) {
	g := &gatedTracker{countingTracker: newCounting(issue("1", "Todo"), issue("2", "Todo"))}
	_, s, err := store.Wrap(g, storeConfig(t, t.TempDir()))
	require.NoError(t, err)
	got, err := s.FetchCandidateIssues(t.Context()) // the cold pull, not gated
	require.NoError(t, err)
	require.Len(t, got, 2)

	// A second pull reads issue 1, then the removal lands before it commits.
	entered, release := g.arm()
	pulled := make(chan error, 1)
	go func() { pulled <- s.RequestSyncNowForTest(t.Context()) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the pull never reached the tracker")
	}
	s.Forget("1")
	close(release)
	require.NoError(t, <-pulled)
	got, _ = s.FetchCandidateIssues(t.Context())
	assert.Equal(t, []string{"ENG-2"}, identifiers(got), "the pull that read issue 1 before its removal does not bring it back")
}

// A removal for an issue the store does not hold yet (the first pull, or a
// pull that finds a new issue, is still in flight) is remembered: the pull
// that read the issue before the removal does not add it.
func TestForgetDuringTheFirstPull(t *testing.T) {
	g := &gatedTracker{countingTracker: newCounting(issue("1", "Todo"), issue("2", "Todo"))}
	_, s, err := store.Wrap(g, storeConfig(t, t.TempDir()))
	require.NoError(t, err)
	entered, release := g.arm()
	pulled := make(chan error, 1)
	go func() { _, err := s.FetchCandidateIssues(t.Context()); pulled <- err }() // the cold pull
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the pull never reached the tracker")
	}
	s.Forget("1")
	close(release)
	require.NoError(t, <-pulled)
	got, err := s.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-2"}, identifiers(got), "the deleted issue is not resurrected by the pull in flight")
}

// gate holds one call, once armed, after it has read and before it returns.
type gate struct {
	mu               sync.Mutex
	entered, release chan struct{}
}

func (g *gate) arm() (<-chan struct{}, chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.entered, g.release = make(chan struct{}, 1), make(chan struct{})
	return g.entered, g.release
}

func (g *gate) hold() {
	g.mu.Lock()
	entered, release := g.entered, g.release
	g.entered, g.release = nil, nil
	g.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
		<-release
	}
}

// gatedScoped is a scope-aware adapter whose by-ID reads can be held.
type gatedScoped struct {
	*scopedTracker
	gate
	outOfScope atomic.Bool // every issue reads as out of scope
}

func (g *gatedScoped) FetchIssuesInScope(ctx context.Context, ids []string) ([]domain.Issue, error) {
	out, err := g.scopedTracker.FetchIssuesInScope(ctx, ids)
	if g.outOfScope.Load() {
		out = nil
	}
	g.hold()
	return out, err
}

// gatedDetail is an adapter without a scope check whose detail reads can be
// held (RefreshIssues' fallback path).
type gatedDetail struct {
	*countingTracker
	gate
}

func (g *gatedDetail) FetchIssueDetail(ctx context.Context, id string) (*domain.Issue, error) {
	out, err := g.countingTracker.FetchIssueDetail(ctx, id)
	g.hold()
	return out, err
}

// A webhook refresh that read an issue before a local write through the
// store does not undo that write when its older answer comes back, on both
// RefreshIssues paths; nor does it re-add an issue removed meanwhile.
func TestStaleRefreshDoesNotUndoANewerLocalChange(t *testing.T) {
	refreshDuring := func(t *testing.T, s *store.Store, g *gate, local func()) {
		t.Helper()
		entered, release := g.arm()
		done := make(chan error, 1)
		go func() { _, err := s.RefreshIssues(t.Context(), []string{"1"}); done <- err }()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the refresh never reached the tracker")
		}
		local()
		close(release)
		require.NoError(t, <-done)
	}
	state := func(t *testing.T, s *store.Store) map[string]string {
		t.Helper()
		got, err := s.FetchIssuesByStates(t.Context(), board)
		require.NoError(t, err)
		out := map[string]string{}
		for _, iss := range got {
			out[iss.ID] = iss.State
		}
		return out
	}
	for _, path := range []string{"scoped", "detail"} {
		t.Run(path, func(t *testing.T) {
			var up tracker.Tracker
			var g *gate
			switch path {
			case "scoped":
				gs := &gatedScoped{scopedTracker: &scopedTracker{countingTracker: newCounting(issue("1", "Todo"), issue("2", "Todo"))}}
				up, g = gs, &gs.gate
			default:
				gd := &gatedDetail{countingTracker: newCounting(issue("1", "Todo"), issue("2", "Todo"))}
				up, g = gd, &gd.gate
			}
			_, s, err := store.Wrap(up, storeConfig(t, t.TempDir()))
			require.NoError(t, err)
			_, err = s.FetchCandidateIssues(t.Context())
			require.NoError(t, err)

			refreshDuring(t, s, g, func() { require.NoError(t, s.UpdateIssueState(t.Context(), "1", "Done")) })
			assert.Equal(t, "Done", state(t, s)["1"], "the newer local write stands")

			if gs, ok := up.(*gatedScoped); ok {
				// An older read that found the issue out of scope does not
				// drop a write made after it.
				gs.outOfScope.Store(true)
				refreshDuring(t, s, g, func() { require.NoError(t, s.UpdateIssueState(t.Context(), "1", "In Progress")) })
				gs.outOfScope.Store(false)
				assert.Equal(t, "In Progress", state(t, s)["1"], "the newer local write stands over the older removal")
			}

			refreshDuring(t, s, g, func() { s.Forget("1") })
			assert.NotContains(t, state(t, s), "1", "the removal made meanwhile stands")
		})
	}
}
