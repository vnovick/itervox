package hub

import (
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// stripKinds rewrites a member's tokens.json the way a member on an older
// binary does (a lastUsed flush, a create or a revoke): every field it
// knows is kept, the kind it does not know is dropped.
func stripKinds(t *testing.T, path string) {
	t.Helper()
	// An older member writes under the store's lock too; without it this
	// rewrite races the member's own writes (a lastUsed flush read before
	// the strip writes the kind back after it).
	unlock := lockTokenStore(t, path)
	defer unlock()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var f map[string]any
	require.NoError(t, json.Unmarshal(raw, &f))
	for _, tok := range f["tokens"].([]any) {
		delete(tok.(map[string]any), "kind")
	}
	out, err := json.MarshalIndent(f, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o600))
}

// TestHubRestampsACachedMemberTokensKindOnRefresh (#334): an older member's
// rewrite of tokens.json drops the kind of the hub's member token while the
// hub keeps the token cached (no 401, no hub restart). The next member
// refresh runs MemberTokenCheck for each cached token, which re-stamps the
// kind: no cache miss happens (the TokenFunc is not asked again), the
// secret and token stay the same and nothing is revoked. Scopes the hub
// holds no cached token of are not checked.
func TestHubRestampsACachedMemberTokensKindOnRefresh(t *testing.T) {
	m := newFakeMember(t, "alpha", time.Now())
	reg := &memberList{}
	reg.add(m.rec)

	var (
		mu       sync.Mutex
		issued   = map[apitokens.Scope]string{}
		lookups  atomic.Int32
		checked  = map[apitokens.Scope]int{}
		restamps atomic.Int32
	)
	tokenFn := func(rec MemberRecord, scope apitokens.Scope) (string, error) {
		lookups.Add(1)
		_, secret, err := apitokens.Open(apitokens.Path(rec.Workflow)).CreateKind("hub-"+string(scope), scope, apitokens.KindHub, "test")
		mu.Lock()
		issued[scope] = secret
		mu.Unlock()
		return secret, err
	}
	// The check cmd/itervox wires (hubCheckMemberToken), reduced to the
	// part that matters here: re-stamp the remembered token, keep it.
	check := func(rec MemberRecord, scope apitokens.Scope) {
		mu.Lock()
		checked[scope]++
		secret := issued[scope]
		mu.Unlock()
		store := apitokens.Open(apitokens.Path(rec.Workflow))
		tok, ok := store.Authenticate(secret)
		if !ok || tok.Kind != "" {
			return
		}
		if _, err := store.SetKind(tok.ID, apitokens.KindHub, "test"); err == nil {
			restamps.Add(1)
		}
	}
	th := startTestHub(t, Config{Members: reg.list, MemberToken: tokenFn, MemberTokenCheck: check})

	eventually(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return checked[apitokens.ScopeViewer] > 0
	}, "the cached viewer token is checked on a refresh")
	require.Equal(t, int32(1), lookups.Load(), "one look-up: the viewer token")
	mu.Lock()
	viewer := issued[apitokens.ScopeViewer]
	assert.Zero(t, checked[apitokens.ScopeOperator], "no operator token is cached, so none is checked")
	mu.Unlock()
	before, ok := apitokens.Open(m.store.FilePath()).Authenticate(viewer)
	require.True(t, ok)
	require.Equal(t, apitokens.KindHub, before.Kind)

	for round := range 2 { // and again after another old-format rewrite
		stripKinds(t, m.store.FilePath())
		stripped, ok := apitokens.Open(m.store.FilePath()).Authenticate(viewer)
		require.True(t, ok)
		require.Empty(t, stripped.Kind, "the old-format rewrite dropped the kind")

		eventually(t, 5*time.Second, func() bool {
			tok, ok := apitokens.Open(m.store.FilePath()).Authenticate(viewer)
			return ok && tok.Kind == apitokens.KindHub && restamps.Load() >= int32(round+1)
		}, "the refresh re-stamps the cached token's kind")
		// Two refreshes can overlap (a rescan and a registry change) and
		// both find the kind empty; SetKind is idempotent, so both succeed.
		// What matters is that each rewrite was answered with a re-stamp.
		assert.GreaterOrEqual(t, restamps.Load(), int32(round+1))
		assert.Equal(t, int32(1), lookups.Load(), "no cache miss: the cached secret is kept")
		reader := apitokens.Open(m.store.FilePath())
		tok, ok := reader.Authenticate(viewer)
		require.True(t, ok, "the secret still authenticates")
		assert.Equal(t, before.ID, tok.ID, "the same token, not a replacement")
		list, err := reader.List()
		require.NoError(t, err)
		assert.Len(t, list, 1, "nothing created, nothing revoked")
	}
	_ = th
}
