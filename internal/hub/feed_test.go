package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

func hubFeed(t *testing.T, th *testHub, since int64) FeedPage {
	t.Helper()
	resp := doHub(t, th.ts.URL+"/hub/api/v1/feed?since="+strconv.FormatInt(since, 10), http.MethodGet, th.secrets[apitokens.ScopeViewer], nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var p FeedPage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&p))
	return p
}

type evKey struct {
	project, kind, id string
}

func keysOf(evs []Event) []evKey {
	out := make([]evKey, 0, len(evs))
	for _, e := range evs {
		out = append(out, evKey{e.Project, e.Kind, e.Identifier})
	}
	return out
}

// TestHubFeedMergesMembersAndResumesFromCursor (#228): the merged feed
// carries every member's new events, each with its project, in one
// increasing hub sequence; history from before a member process started is
// not news. After a restart the next hub resumes from the per-member
// cursors in hub_feed_cursors.json: no repeats, nothing missed, and the hub
// sequence keeps growing.
func TestHubFeedMergesMembersAndResumesFromCursor(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	alpha := newFakeMember(t, "alpha", started)
	beta := newFakeMember(t, "beta", started)
	alpha.disp.add("run_failed", "OLD-1", started.Add(-time.Hour)) // before this alpha process: history
	alpha.disp.add("run_started", "ENG-1", started.Add(time.Second))
	reg := &memberList{}
	reg.add(alpha.rec)
	reg.add(beta.rec)
	stateDir := filepath.Join(t.TempDir(), "hub")

	th := startTestHub(t, Config{StateDir: stateDir, Members: reg.list})
	now := time.Now()
	beta.disp.add("input_required", "OPS-7", now)
	alpha.disp.add("run_finished", "ENG-1", now)
	var page FeedPage
	eventually(t, 5*time.Second, func() bool { page = hubFeed(t, th, 0); return len(page.Events) == 3 }, "three new events merged")
	assert.ElementsMatch(t, []evKey{
		{"alpha", "run_started", "ENG-1"}, {"beta", "input_required", "OPS-7"}, {"alpha", "run_finished", "ENG-1"},
	}, keysOf(page.Events))
	for i, e := range page.Events {
		assert.Equal(t, int64(i+1), e.Seq, "one increasing hub sequence")
		assert.NotEmpty(t, e.Project)
		assert.NotZero(t, e.MemberSeq)
	}
	headBefore := page.HeadSeq
	assert.Equal(t, int64(3), headBefore)
	// Cursor reads, as on a member.
	assert.Len(t, hubFeed(t, th, 2).Events, 1)

	th.close() // the hub stops; its cursors are saved
	info, err := os.Stat(filepath.Join(stateDir, CursorFileName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Events while no hub runs.
	beta.disp.add("pr_merged", "OPS-7", time.Now())
	alpha.disp.add("review_verdict", "ENG-1", time.Now())

	th2 := startTestHub(t, Config{StateDir: stateDir, Members: reg.list})
	eventually(t, 5*time.Second, func() bool { page = hubFeed(t, th2, 0); return len(page.Events) >= 2 }, "the missed events are caught up")
	time.Sleep(300 * time.Millisecond) // more poll rounds: nothing is repeated
	page = hubFeed(t, th2, 0)
	assert.ElementsMatch(t, []evKey{{"beta", "pr_merged", "OPS-7"}, {"alpha", "review_verdict", "ENG-1"}}, keysOf(page.Events))
	for _, e := range page.Events {
		assert.Greater(t, e.Seq, headBefore, "the hub sequence continues across restarts")
	}
}

// recordingNotifier fails its first failures calls, then records.
type recordingNotifier struct {
	name     string
	mu       sync.Mutex
	failures int
	calls    []evKey
	got      []evKey
	disabled string // a kind Enabled refuses
}

func (n *recordingNotifier) Name() string { return n.name }

func (n *recordingNotifier) Enabled(_, kind string) bool { return kind != n.disabled }

func (n *recordingNotifier) Notify(_ context.Context, ev Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	k := evKey{ev.Project, ev.Kind, ev.Identifier}
	n.calls = append(n.calls, k)
	if n.failures > 0 {
		n.failures--
		return errors.New("channel down")
	}
	n.got = append(n.got, k)
	return nil
}

func (n *recordingNotifier) delivered() []evKey {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]evKey(nil), n.got...)
}

func (n *recordingNotifier) attempts() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.calls)
}

// TestNotifierReceivesEachEventAtLeastOnce (#228): every merged event a
// notifier enables is delivered, in hub order, retried with backoff after a
// failure, never skipped; a kind it does not enable is never sent. An event
// still failing when the hub stops is delivered by the next hub (the
// per-notifier cursor moved only on success).
func TestNotifierReceivesEachEventAtLeastOnce(t *testing.T) {
	alpha := newFakeMember(t, "alpha", time.Now().Add(-time.Minute))
	reg := &memberList{}
	reg.add(alpha.rec)
	stateDir := filepath.Join(t.TempDir(), "hub")
	n := &recordingNotifier{name: "test", failures: 2, disabled: "dispatch_paused"}
	th := startTestHub(t, Config{StateDir: stateDir, Members: reg.list, Notifiers: []Notifier{n, LogNotifier{}}, NotifierBackoff: 20 * time.Millisecond})
	eventually(t, 5*time.Second, func() bool { _, ok := th.h.members.get("alpha"); return ok }, "alpha discovered")
	time.Sleep(200 * time.Millisecond) // the first poll sets alpha's cursor
	alpha.disp.add("input_required", "ENG-1", time.Now())
	alpha.disp.add("dispatch_paused", "", time.Now())
	alpha.disp.add("run_failed", "ENG-2", time.Now())
	want := []evKey{{"alpha", "input_required", "ENG-1"}, {"alpha", "run_failed", "ENG-2"}}
	eventually(t, 5*time.Second, func() bool { return len(n.delivered()) == 2 }, "both enabled events delivered")
	assert.Equal(t, want, n.delivered(), "in hub order, the disabled kind skipped")
	assert.Equal(t, 4, n.attempts(), "the first event was retried twice before it went through")

	// A notifier whose channel stays down when the hub stops.
	down := &recordingNotifier{name: "test", failures: 1 << 30}
	th.close()
	th2 := startTestHub(t, Config{StateDir: stateDir, Members: reg.list, Notifiers: []Notifier{down}, NotifierBackoff: 20 * time.Millisecond})
	alpha.disp.add("guardrail_approval", "ENG-3", time.Now())
	eventually(t, 5*time.Second, func() bool { return down.attempts() >= 2 }, "the undeliverable event is retried")
	th2.close()

	up := &recordingNotifier{name: "test"}
	startTestHub(t, Config{StateDir: stateDir, Members: reg.list, Notifiers: []Notifier{up}, NotifierBackoff: 20 * time.Millisecond})
	eventually(t, 5*time.Second, func() bool { return len(up.delivered()) == 1 }, "the next hub delivers it")
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, []evKey{{"alpha", "guardrail_approval", "ENG-3"}}, up.delivered(), "and only it: delivered events are not repeated")
}
