package hub

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// fakeInteractive is an InteractiveChannel that keeps the MemberActions
// the hub hands it.
type fakeInteractive struct {
	mu      sync.Mutex
	actions MemberActions
	runs    int
}

func (*fakeInteractive) Name() string                             { return ChannelSlack }
func (*fakeInteractive) Send(context.Context, Notification) error { return nil }
func (f *fakeInteractive) Run(ctx context.Context, a MemberActions) {
	f.mu.Lock()
	f.actions, f.runs = a, f.runs+1
	f.mu.Unlock()
	<-ctx.Done()
}

func (f *fakeInteractive) get() (MemberActions, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.actions, f.runs
}

// TestHubMemberActionsCallThroughRouteTable (#230): an interactive channel
// is run once while the hub serves, and its member actions reach exactly
// the named member with the hub's operator token, for routes the member
// declares viewer or operator only; an admin or agent-grant route, an
// unknown route and a member that was replaced are refused before any
// request is sent.
func TestHubMemberActionsCallThroughRouteTable(t *testing.T) {
	a := newFakeMember(t, "alpha", time.Now())
	b := newFakeMember(t, "beta", time.Now())
	members := &memberList{}
	members.add(a.rec)
	members.add(b.rec)
	ch := &fakeInteractive{}
	th := startTestHub(t, Config{Members: members.list, Channels: []Channel{ch}})
	eventually(t, 5*time.Second, func() bool {
		act, _ := ch.get()
		if act == nil {
			return false
		}
		ref, ok := act.Member("alpha")
		return ok && ref.Workflow == a.workflow
	}, "the channel runs and sees the members")
	act, runs := ch.get()
	assert.Equal(t, 1, runs)
	assert.Len(t, act.Projects(), 2)
	ref, _ := act.Member("alpha")
	eventually(t, 5*time.Second, func() bool { m, ok := th.h.members.get("alpha"); return ok && m.routes != nil }, "route table")

	bBefore := b.writes.Load()
	status, _, err := act.Call(t.Context(), ref, http.MethodPost, "/api/v1/issues/ENG-1/provide-input", map[string]string{"message": "yes"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, status)
	wa, _ := a.writeAuth.Load().(string)
	secret := strings.TrimPrefix(wa, "Bearer ")
	tok, ok := a.store.Authenticate(secret)
	require.True(t, ok, "the member accepted the hub's own member token")
	assert.Equal(t, apitokens.ScopeOperator, tok.Scope)
	assert.Equal(t, bBefore, b.writes.Load(), "the other member is never called")

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/tokens"},                                  // admin
		{http.MethodPost, "/api/v1/agent-actions/ENG-1/provide-input"},      // agent grant
		{http.MethodPost, "/api/v1/nope"},                                   // undeclared
		{http.MethodPost, "/api/v1/issues/../tokens/provide-input"},         // dot segment
		{http.MethodPost, "/api/v1/issues/ENG-1/provide-input?fromAgent=1"}, // query
	} {
		before := a.writes.Load()
		_, _, err := act.Call(t.Context(), ref, c.method, c.path, nil)
		require.ErrorIs(t, err, ErrMemberActionRefused, c.path)
		assert.Equal(t, before, a.writes.Load(), "refused before reaching the member: %s", c.path)
	}
	stale := MemberRef{Project: "alpha", Workflow: b.workflow}
	_, _, err = act.Call(t.Context(), stale, http.MethodPost, "/api/v1/issues/ENG-1/provide-input", map[string]string{"message": "x"})
	assert.True(t, errors.Is(err, ErrMemberActionRefused), "a project name now naming another member is refused")
	assert.NotEqual(t, ref.Key(), stale.Key())
}
