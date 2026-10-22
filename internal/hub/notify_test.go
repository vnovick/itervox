package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/server"
)

// recordingChannel is a Channel that records what it was sent.
type recordingChannel struct {
	name string
	mu   sync.Mutex
	sent []Notification
	fail error
}

func (c *recordingChannel) Name() string { return c.name }

func (c *recordingChannel) Send(_ context.Context, n Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != nil {
		return c.fail
	}
	c.sent = append(c.sent, n)
	return nil
}

func (c *recordingChannel) got() []Notification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Notification(nil), c.sent...)
}

func titles(ns []Notification) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Title)
	}
	return out
}

// fakeClock is a settable clock for channel notifiers.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestChannelNotifier wires a recording channel to settings s over a
// fake clock.
func newTestChannelNotifier(t *testing.T, name string, s NotificationSettings) (*channelNotifier, *recordingChannel, *fakeClock, *notifySettingsStore) {
	t.Helper()
	store := loadNotifySettings(filepath.Join(t.TempDir(), NotificationsFileName))
	_, err := store.put(s)
	require.NoError(t, err)
	ch := &recordingChannel{name: name}
	clock := &fakeClock{t: time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)}
	cn := newChannelNotifier(ch, store)
	cn.now = clock.now
	return cn, ch, clock, store
}

func feedEv(project, kind, identifier string, payload string) Event {
	ev := Event{Project: project, Kind: kind, Identifier: identifier, TS: time.Now()}
	if payload != "" {
		ev.Payload = json.RawMessage(payload)
	}
	return ev
}

// TestNotificationSettingsRouteKindsToChannels (#229): every notification
// kind maps from its #213 feed kind (a guardrail approval is an
// input_required event with payload kind guardrail_approval); by default
// input required, guardrail approval and run failed go to every enabled
// channel and every other kind is off; a project's route overrides the
// default per kind (an empty list turns it off); a channel switched off
// receives nothing; and the hub delivers each routed event to exactly the
// channels the settings name.
func TestNotificationSettingsRouteKindsToChannels(t *testing.T) {
	// Feed kind → notification kind.
	assert.Equal(t, KindInputRequired, NotificationKindOf(feedEv("p", "input_required", "A-1", `{"runKind":"worker"}`)))
	assert.Equal(t, KindGuardrailApproval, NotificationKindOf(feedEv("p", "input_required", "A-1", `{"kind":"guardrail_approval"}`)))
	for feed, want := range map[string]string{
		"run_failed": KindRunFailed, "review_verdict": KindReviewVerdict, "pr_merged": KindPRMerged,
		"lesson_proposed": KindLessonProposed, "dispatch_paused": KindDispatchPaused,
		"run_started": "", "run_finished": "", "input_answered": "", "pr_opened": "", "dispatch_resumed": "",
	} {
		assert.Equal(t, want, NotificationKindOf(feedEv("p", feed, "A-1", "")), feed)
	}

	now := time.Now()
	s := DefaultNotificationSettings()
	all := []string{ChannelPush, ChannelSlack, ChannelDesktop}
	for _, k := range []string{KindInputRequired, KindGuardrailApproval, KindRunFailed} {
		assert.Equal(t, all, s.Route("any", k, now), "%s defaults to every channel", k)
	}
	for _, k := range []string{KindReviewVerdict, KindPRMerged, KindLessonProposed, KindDispatchPaused} {
		assert.Empty(t, s.Route("any", k, now), "%s is off by default", k)
	}
	assert.Nil(t, s.Route("any", "budget_hit", now), "an unknown kind routes nowhere")

	s.Projects["alpha"] = ProjectNotificationSettings{Routes: map[string][]string{
		KindPRMerged:  {ChannelDesktop, ChannelSlack},
		KindRunFailed: {},
	}}
	s.Channels[ChannelPush] = ChannelSettings{Enabled: false}
	assert.Equal(t, []string{ChannelSlack, ChannelDesktop}, s.Route("alpha", KindPRMerged, now), "a project route overrides the default, in registry order")
	assert.Empty(t, s.Route("alpha", KindRunFailed, now), "an empty project route turns the kind off")
	assert.Equal(t, []string{ChannelSlack, ChannelDesktop}, s.Route("alpha", KindInputRequired, now), "the default applies; push is switched off")
	assert.Equal(t, []string{ChannelSlack, ChannelDesktop}, s.Route("beta", KindRunFailed, now), "another project keeps the defaults")

	// End to end: a hub with Slack and push channels set up, settings in
	// notifications.json, and one member's feed.
	started := time.Now().Add(-time.Minute)
	alpha := newFakeMember(t, "alpha", started)
	reg := &memberList{}
	reg.add(alpha.rec)
	stateDir := filepath.Join(t.TempDir(), "hub")
	require.NoError(t, os.MkdirAll(stateDir, 0o700))
	data, err := json.Marshal(s)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, NotificationsFileName), data, 0o600))
	slack, push := &recordingChannel{name: ChannelSlack}, &recordingChannel{name: ChannelPush}
	th := startTestHub(t, Config{StateDir: stateDir, Members: reg.list, Channels: []Channel{slack, push}})

	// Learn the member first so later events are news.
	alpha.disp.add("run_started", "ENG-0", time.Now())
	eventually(t, 5*time.Second, func() bool { return th.h.feed.headSeq() == 1 }, "member known")
	alpha.disp.add("input_required", "ENG-1", time.Now())
	eventually(t, 5*time.Second, func() bool { return len(slack.got()) == 1 }, "input_required reached slack")
	assert.Equal(t, []string{"ENG-1 needs input"}, titles(slack.got()))
	assert.Empty(t, push.got(), "push is switched off in the settings")
	assert.Equal(t, "/hub/p/alpha/?issue=ENG-1", slack.got()[0].URL)
	assert.True(t, slack.got()[0].Urgent)

	// The feed carries each event's notification kind and channels.
	page := hubFeed(t, th, 0)
	require.Len(t, page.Events, 2)
	assert.Empty(t, page.Events[0].Channels, "run_started notifies nobody")
	assert.Equal(t, KindInputRequired, page.Events[1].NotifyKind)
	assert.Equal(t, []string{ChannelSlack, ChannelDesktop}, page.Events[1].Channels)
}

// TestUrgentKindsAreNeverBatched (#229): input required and guardrail
// approval are sent the moment they arrive, even while a digest of
// low-priority kinds is pending, and never appear in that digest.
func TestUrgentKindsAreNeverBatched(t *testing.T) {
	s := DefaultNotificationSettings()
	s.Defaults[KindPRMerged] = []string{ChannelDesktop}
	cn, ch, clock, _ := newTestChannelNotifier(t, ChannelDesktop, s)
	ctx := context.Background()

	assert.True(t, IsUrgentKind(KindInputRequired))
	assert.True(t, IsUrgentKind(KindGuardrailApproval))
	assert.False(t, IsUrgentKind(KindRunFailed))

	require.NoError(t, cn.Notify(ctx, feedEv("alpha", "run_failed", "ENG-1", "")))
	require.NoError(t, cn.Notify(ctx, feedEv("alpha", "pr_merged", "ENG-2", "")))
	assert.Empty(t, ch.got(), "low-priority kinds wait for the digest")

	require.NoError(t, cn.Notify(ctx, feedEv("alpha", "input_required", "ENG-3", "")))
	require.NoError(t, cn.Notify(ctx, feedEv("beta", "input_required", "OPS-1", `{"kind":"guardrail_approval"}`)))
	assert.Equal(t, []string{"ENG-3 needs input", "OPS-1 needs approval"}, titles(ch.got()), "urgent kinds go at once")

	// An urgent send that fails is returned, so the hub retries it.
	ch.fail = errors.New("down")
	assert.Error(t, cn.Notify(ctx, feedEv("alpha", "input_required", "ENG-4", "")))
	ch.fail = nil

	clock.advance(time.Duration(DefaultDigestIntervalMinutes) * time.Minute)
	cn.flushDue(ctx)
	got := ch.got()
	require.Len(t, got, 3)
	digest := got[2]
	assert.Equal(t, NotificationDigest, digest.Kind)
	assert.Equal(t, []string{"ENG-1 run failed", "ENG-2 PR merged"}, titles(digest.Items))
	for _, it := range digest.Items {
		assert.False(t, IsUrgentKind(it.Kind), "an urgent kind was batched: %s", it.Title)
	}
}

// TestSnoozeSuppressesUntil (#229): a snoozed project notifies nobody until
// its snooze_until passes — Enabled turns its events down, Notify drops
// them, and digest items of a project snoozed meanwhile are dropped — and
// it notifies again from then on.
func TestSnoozeSuppressesUntil(t *testing.T) {
	cn, ch, clock, store := newTestChannelNotifier(t, ChannelDesktop, DefaultNotificationSettings())
	ctx := context.Background()
	until := clock.now().Add(time.Hour)
	s := store.get()
	s.Projects["alpha"] = ProjectNotificationSettings{SnoozeUntil: &until}
	_, err := store.put(s)
	require.NoError(t, err)

	assert.False(t, cn.Enabled("alpha", "input_required"), "snoozed: the event is skipped")
	assert.True(t, cn.Enabled("beta", "input_required"), "another project is not snoozed")
	require.NoError(t, cn.Notify(ctx, feedEv("alpha", "input_required", "ENG-1", "")))
	require.NoError(t, cn.Notify(ctx, feedEv("beta", "input_required", "OPS-1", "")))
	assert.Equal(t, []string{"OPS-1 needs input"}, titles(ch.got()))

	clock.advance(59 * time.Minute)
	assert.False(t, cn.Enabled("alpha", "input_required"), "still snoozed a minute before")

	clock.advance(time.Minute)
	assert.True(t, cn.Enabled("alpha", "input_required"), "the snooze ended")
	require.NoError(t, cn.Notify(ctx, feedEv("alpha", "input_required", "ENG-2", "")))
	assert.Equal(t, []string{"OPS-1 needs input", "ENG-2 needs input"}, titles(ch.got()))

	// A digest item of a project snoozed before the digest goes is dropped.
	require.NoError(t, cn.Notify(ctx, feedEv("alpha", "run_failed", "ENG-3", "")))
	later := clock.now().Add(2 * time.Hour)
	s = store.get()
	s.Projects["alpha"] = ProjectNotificationSettings{SnoozeUntil: &later}
	_, err = store.put(s)
	require.NoError(t, err)
	clock.advance(time.Duration(DefaultDigestIntervalMinutes) * time.Minute)
	cn.flushDue(ctx)
	assert.Len(t, ch.got(), 2, "the snoozed project's digest item was dropped")
}

// TestDigestBatchesLowPriority (#229): low-priority kinds collect for one
// digest interval from the first of them (digest_interval_minutes) and go
// out as one digest; nothing is sent before; a window of one item is sent
// as that item; a failed digest is kept and sent at the next tick.
func TestDigestBatchesLowPriority(t *testing.T) {
	s := DefaultNotificationSettings()
	s.DigestIntervalMinutes = 10
	s.Defaults[KindReviewVerdict] = []string{ChannelSlack}
	s.Defaults[KindLessonProposed] = []string{ChannelSlack}
	cn, ch, clock, _ := newTestChannelNotifier(t, ChannelSlack, s)
	ctx := context.Background()

	require.NoError(t, cn.Notify(ctx, feedEv("alpha", "run_failed", "ENG-1", "")))
	clock.advance(4 * time.Minute)
	require.NoError(t, cn.Notify(ctx, feedEv("alpha", "review_verdict", "ENG-2", `{"outcome":"passed"}`)))
	require.NoError(t, cn.Notify(ctx, feedEv("beta", "lesson_proposed", "OPS-1", "")))
	require.NoError(t, cn.Notify(ctx, feedEv("beta", "pr_merged", "OPS-2", "")), "pr_merged is off by default: not batched")
	clock.advance(5 * time.Minute)
	cn.flushDue(ctx)
	assert.Empty(t, ch.got(), "nothing before the interval")

	clock.advance(time.Minute) // 10 minutes after the first item
	cn.flushDue(ctx)
	got := ch.got()
	require.Len(t, got, 1, "one digest")
	d := got[0]
	assert.Equal(t, NotificationDigest, d.Kind)
	assert.Equal(t, ChannelSlack, d.Channel)
	assert.Equal(t, "3 updates", d.Title)
	assert.Equal(t, "across 2 projects", d.Body)
	assert.Equal(t, []string{"ENG-1 run failed", "ENG-2 review passed", "Lesson proposed OPS-1"}, titles(d.Items))
	cn.flushDue(ctx)
	assert.Len(t, ch.got(), 1, "the digest is not sent twice")

	// A window of one is sent as that item.
	require.NoError(t, cn.Notify(ctx, feedEv("alpha", "run_failed", "ENG-9", "")))
	clock.advance(10 * time.Minute)
	ch.fail = errors.New("slack down")
	cn.flushDue(ctx)
	ch.fail = nil
	assert.Len(t, ch.got(), 1, "a failed digest is not lost")
	cn.flushDue(ctx)
	got = ch.got()
	require.Len(t, got, 2)
	assert.Equal(t, KindRunFailed, got[1].Kind)
	assert.Equal(t, "ENG-9 run failed", got[1].Title)
}

// TestNotificationSettingsAPI (#229): GET and PUT /hub/api/v1/notifications
// need an admin hub token, the test route an operator one; PUT validates,
// saves notifications.json 0600 and applies it; the test route sends
// through the named channel and refuses a channel that is not set up.
func TestNotificationSettingsAPI(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "hub")
	th := startTestHub(t, Config{StateDir: stateDir})
	base := th.ts.URL + "/hub/api/v1/notifications"

	resp := doHub(t, base, http.MethodGet, th.secrets[apitokens.ScopeOperator], nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp = doHub(t, base, http.MethodGet, th.secrets[apitokens.ScopeAdmin], nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got NotificationsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, DefaultDigestIntervalMinutes, got.Settings.DigestIntervalMinutes)
	assert.Equal(t, []NotificationChannelInfo{{ChannelPush, false}, {ChannelSlack, false}, {ChannelDesktop, true}}, got.Channels)
	assert.Len(t, got.Kinds, len(NotificationKinds))

	body := `{"digest_interval_minutes": 30, "projects": {"alpha": {"routes": {"pr_merged": ["desktop"]}, "snooze_until": "2030-01-01T00:00:00Z"}}}`
	resp = doHub(t, base, http.MethodPut, th.secrets[apitokens.ScopeOperator], strings.NewReader(body))
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp = doHub(t, base, http.MethodPut, th.secrets[apitokens.ScopeAdmin], strings.NewReader(`{"defaults": {"pr_merged": ["pager"]}}`))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp = doHub(t, base, http.MethodPut, th.secrets[apitokens.ScopeAdmin], strings.NewReader(body))
	require.Equal(t, http.StatusOK, resp.StatusCode, readBody(t, resp))
	path := filepath.Join(stateDir, NotificationsFileName)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	reloaded := loadNotifySettings(path).get()
	assert.Equal(t, 30, reloaded.DigestIntervalMinutes)
	assert.True(t, reloaded.Snoozed("alpha", time.Now()))
	assert.Equal(t, 30*time.Minute, th.h.notify.digestInterval(), "applied at once")

	test := base + "/test"
	resp = doHub(t, test, http.MethodPost, th.secrets[apitokens.ScopeViewer], strings.NewReader(`{"channel":"desktop"}`))
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp = doHub(t, test, http.MethodPost, th.secrets[apitokens.ScopeOperator], strings.NewReader(`{"channel":"slack"}`))
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	resp = doHub(t, test, http.MethodPost, th.secrets[apitokens.ScopeOperator], strings.NewReader(`{"channel":"fax"}`))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp = doHub(t, test, http.MethodPost, th.secrets[apitokens.ScopeOperator], strings.NewReader(`{"channel":"desktop"}`))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	notes, _ := th.h.desktop.after(0)
	require.Len(t, notes, 1)
	assert.Equal(t, NotificationTest, notes[0].Kind)
}

// TestDesktopNotificationsStreamOnHubEvents (#229): the desktop channel's
// notifications go out on GET /hub/api/v1/events as `event: notification`
// frames with an id; only events the settings route to desktop do; and a
// reconnect with notify_since catches up without repeats.
func TestDesktopNotificationsStreamOnHubEvents(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	alpha := newFakeMember(t, "alpha", started)
	reg := &memberList{}
	reg.add(alpha.rec)
	th := startTestHub(t, Config{Members: reg.list})
	alpha.disp.add("run_started", "ENG-0", time.Now())
	eventually(t, 5*time.Second, func() bool { return th.h.feed.headSeq() == 1 }, "member known")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, th.ts.URL+"/hub/api/v1/events", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+th.secrets[apitokens.ScopeViewer])
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	frames := make(chan [2]string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var event, id string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: ") && event == "notification":
				frames <- [2]string{id, strings.TrimPrefix(line, "data: ")}
			case line == "":
				event, id = "", ""
			}
		}
	}()

	alpha.disp.add("pr_merged", "ENG-1", time.Now()) // off by default: no notification
	alpha.disp.mu.Lock()
	alpha.disp.seq++
	alpha.disp.events = append(alpha.disp.events, server.FeedEvent{Seq: alpha.disp.seq, TS: time.Now(), Kind: "input_required",
		Identifier: "ENG-2", Payload: server.FeedPayload{Kind: "guardrail_approval"}})
	alpha.disp.mu.Unlock()
	var f [2]string
	select {
	case f = <-frames:
	case <-ctx.Done():
		t.Fatal("no notification frame")
	}
	assert.Equal(t, "1", f[0])
	var n Notification
	require.NoError(t, json.Unmarshal([]byte(f[1]), &n))
	assert.Equal(t, ChannelDesktop, n.Channel)
	assert.Equal(t, KindGuardrailApproval, n.Kind)
	assert.Equal(t, "ENG-2 needs approval", n.Title)
	assert.Equal(t, "alpha", n.Body)
	assert.Equal(t, "/hub/p/alpha/?issue=ENG-2", n.URL)
	select {
	case extra := <-frames:
		t.Fatalf("unexpected frame %v", extra)
	case <-time.After(300 * time.Millisecond):
	}

	// A reconnect with notify_since replays what came after, once.
	require.NoError(t, th.h.desktop.Send(ctx, Notification{Kind: NotificationTest, Title: "t"}))
	notes, _ := th.h.desktop.after(1)
	require.Len(t, notes, 1)
	assert.Equal(t, int64(2), notes[0].ID)
	notes, _ = th.h.desktop.after(99) // the hub restarted: ids began again
	assert.Len(t, notes, 2)
}
