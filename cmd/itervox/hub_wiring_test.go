//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/hub"
)

// resetHubParticipation clears the process-wide hub state between tests.
func resetHubParticipation(t *testing.T) {
	t.Helper()
	stopHub()
	hubOnce = sync.Once{}
	hubMembership.Store(false)
	hubStopMu.Lock()
	hubCancel, hubDone = nil, nil
	hubStopMu.Unlock()
}

// writeHubRegistryRecord writes a live registry record for pid whose
// hub_member is member.
func writeHubRegistryRecord(t *testing.T, regDir, wf string, pid int, member bool) {
	t.Helper()
	writeRegistryFixture(t, regDir, wf, pid, false)
	rec := daemonRecord{
		SchemaVersion: daemonRecordSchemaVersion, PID: pid, Workflow: wf, ProjectDir: filepath.Dir(wf),
		URL: "http://127.0.0.1:1/", TokenRequired: true, HubMember: member,
	}
	data, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(regDir, strconv.Itoa(pid)+".json"), data, 0o600))
}

// TestNoHubFlagOptsOut (#228): --no-hub (and hub.enabled: false) keeps a
// daemon out of the hub entirely: it never takes the hub lock, its registry
// record says hub_member: false, and the hub neither lists nor proxies it.
// Without the flag the same daemon takes part (the contrast case).
func TestNoHubFlagOptsOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	regDir := filepath.Join(home, ".itervox", "daemons")
	require.NoError(t, os.MkdirAll(regDir, 0o700))
	wf := filepath.Join(t.TempDir(), "WORKFLOW.md")
	enabled := &config.Config{Hub: config.HubConfig{Enabled: true, Port: 0, Bind: "127.0.0.1"}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() { resetHubParticipation(t) })

	optedOut := func(t *testing.T) {
		t.Helper()
		assert.False(t, hubMembership.Load())
		assert.False(t, newDaemonRecord(wf, "127.0.0.1:8090", t.TempDir(), "tok", false, false).HubMember)
		time.Sleep(1500 * time.Millisecond) // longer than an election poll
		_, held, err := hub.ReadLockInfo(regDir)
		require.NoError(t, err)
		assert.False(t, held, "an opted-out daemon never takes the hub lock")
	}

	t.Run("--no-hub", func(t *testing.T) {
		resetHubParticipation(t)
		maybeStartHub(ctx, enabled, wf, true)
		optedOut(t)
	})
	t.Run("hub.enabled: false", func(t *testing.T) {
		resetHubParticipation(t)
		maybeStartHub(ctx, &config.Config{Hub: config.HubConfig{Enabled: false, Bind: "127.0.0.1"}}, wf, false)
		optedOut(t)
	})
	t.Run("the hub lists only members", func(t *testing.T) {
		sleeper := exec.Command("sleep", "30")
		require.NoError(t, sleeper.Start())
		t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
		memberWF := filepath.Join(t.TempDir(), "member", "WORKFLOW.md")
		optedOutWF := filepath.Join(t.TempDir(), "solo", "WORKFLOW.md")
		writeHubRegistryRecord(t, regDir, memberWF, os.Getpid(), true)
		writeHubRegistryRecord(t, regDir, optedOutWF, sleeper.Process.Pid, false)
		require.Len(t, readDaemonRegistry(regDir), 2, "both daemons are live")
		members := hubMembersIn(regDir)
		require.Len(t, members, 1)
		assert.Equal(t, memberWF, members[0].Workflow)

		var out, errOut bytes.Buffer
		require.Equal(t, 0, hubCmd([]string{"--json"}, &out, &errOut))
		var st hubStatus
		require.NoError(t, json.Unmarshal(out.Bytes(), &st))
		require.Len(t, st.Members, 1)
		assert.Equal(t, "member", st.Members[0].Name)
		_ = os.Remove(filepath.Join(regDir, strconv.Itoa(os.Getpid())+".json"))
		_ = os.Remove(filepath.Join(regDir, strconv.Itoa(sleeper.Process.Pid)+".json"))
	})
	t.Run("without the flag the daemon takes part", func(t *testing.T) {
		resetHubParticipation(t)
		maybeStartHub(ctx, enabled, wf, false)
		assert.True(t, hubMembership.Load())
		assert.True(t, newDaemonRecord(wf, "127.0.0.1:8090", t.TempDir(), "tok", false, false).HubMember)
		var info hub.LockInfo
		require.Eventually(t, func() bool {
			i, ok, _ := hub.ReadLockInfo(regDir)
			info = i
			return ok
		}, 5*time.Second, 50*time.Millisecond, "the only daemon becomes the hub")
		assert.Equal(t, os.Getpid(), info.PID)
		var out, errOut bytes.Buffer
		require.Equal(t, 0, hubCmd(nil, &out, &errOut))
		assert.Contains(t, out.String(), info.URL)
		stopHub()
		_, held, _ := hub.ReadLockInfo(regDir)
		assert.False(t, held, "stopping hands the hub over at once")
	})
}

// TestHubMemberTokenStoredPrivatelyAndReused (#228): the hub's member token
// is created in the member's own token store through the `itervox mcp` code
// path, remembered 0600 in .itervox/hub-<scope>-token.json, gitignored, and
// reused; viewer and operator are separate tokens.
func TestHubMemberTokenStoredPrivatelyAndReused(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "WORKFLOW.md")
	m := hub.MemberRecord{Workflow: wf, ProjectDir: dir}
	op, err := hubMemberToken(m, "operator")
	require.NoError(t, err)
	again, err := hubMemberToken(m, "operator")
	require.NoError(t, err)
	assert.Equal(t, op, again, "a usable remembered token is reused")
	viewer, err := hubMemberToken(m, "viewer")
	require.NoError(t, err)
	assert.NotEqual(t, op, viewer)

	info, err := os.Stat(storedTokenPath(wf, "hub-operator-token.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	gi, err := os.ReadFile(filepath.Join(dir, ".itervox", ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(gi), "/hub-*-token.json")
}

// TestHubMemberTokenIsMarkedAsTheHubs: the hub's member token carries
// apitokens.KindHub (the one principal whose hub caller header a member
// trusts), and a remembered token from before the mark gets the mark and
// keeps its secret.
func TestHubMemberTokenIsMarkedAsTheHubs(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "WORKFLOW.md")
	m := hub.MemberRecord{Workflow: wf, ProjectDir: dir}
	// A token remembered by an older hub: same file and name, no kind.
	old, _, err := ensureStoredToken(wf, storedTokenSpec{file: "hub-operator-token.json", name: "hub-operator",
		actor: "itervox hub", label: "hub"}, apitokens.ScopeOperator)
	require.NoError(t, err)
	op, err := hubMemberToken(m, apitokens.ScopeOperator)
	require.NoError(t, err)
	assert.Equal(t, old, op, "the kindless token is kept")
	store := apitokens.Open(apitokens.Path(wf))
	tok, ok := store.Authenticate(op)
	require.True(t, ok)
	assert.Equal(t, apitokens.KindHub, tok.Kind, "and marked as the hub's")
	// Another client's spec (another kind) still replaces it.
	mcp, created, err := ensureStoredToken(wf, storedTokenSpec{file: "hub-operator-token.json", name: "hub-operator",
		actor: "itervox hub", label: "hub"}, apitokens.ScopeOperator)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, op, mcp)
	_, ok = apitokens.Open(store.FilePath()).Authenticate(op)
	assert.False(t, ok, "the replaced token is revoked")
}

// stripTokenKinds rewrites tokens.json the way a member on an older binary
// does (a lastUsed flush, a create or a revoke): every field it knows is
// kept, the kind it does not know is dropped.
func stripTokenKinds(t *testing.T, path string) {
	t.Helper()
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

// TestHubMemberTokenKindRestampedAfterAnOldFormatRewrite: an older member
// rewriting tokens.json drops the hub token's kind. The hub's next look-up
// (a cache miss) re-stamps it: the same token and secret stay valid, nothing
// is revoked (so no open hub terminal closes), and the kind is back.
func TestHubMemberTokenKindRestampedAfterAnOldFormatRewrite(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "WORKFLOW.md")
	m := hub.MemberRecord{Workflow: wf, ProjectDir: dir}
	op, err := hubMemberToken(m, apitokens.ScopeOperator)
	require.NoError(t, err)
	store := apitokens.Open(apitokens.Path(wf))
	before, ok := store.Authenticate(op)
	require.True(t, ok)
	require.Equal(t, apitokens.KindHub, before.Kind)

	for range 2 { // and again after another old-format rewrite
		stripTokenKinds(t, store.FilePath())
		member := apitokens.Open(store.FilePath()) // a fresh reader of the stripped file
		stripped, ok := member.Authenticate(op)
		require.True(t, ok)
		require.Empty(t, stripped.Kind, "the old-format rewrite dropped the kind")

		again, err := hubMemberToken(m, apitokens.ScopeOperator)
		require.NoError(t, err)
		assert.Equal(t, op, again, "the same secret is kept")
		reader := apitokens.Open(store.FilePath())
		tok, ok := reader.Authenticate(op)
		require.True(t, ok, "the secret still authenticates")
		assert.Equal(t, before.ID, tok.ID, "the same token, not a replacement")
		assert.Equal(t, apitokens.KindHub, tok.Kind, "the kind is re-stamped")
		list, err := reader.List()
		require.NoError(t, err)
		assert.Len(t, list, 1, "nothing created, nothing revoked")
	}
}

// TestHubCheckMemberTokenRestampsWhileCached (#334): the hub keeps its
// member token cached, so after an older member's rewrite drops the kind
// there is no look-up to re-stamp it. hubCheckMemberToken (the hub's
// MemberTokenCheck, run on each member refresh) re-stamps it without a
// look-up: the cached secret, the token id and the token count stay the
// same. It never creates a token (no remembered one: nothing happens) and
// never touches a token it no longer finds usable (a rotated one stays as
// it is, for the next look-up to replace).
func TestHubCheckMemberTokenRestampsWhileCached(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "WORKFLOW.md")
	m := hub.MemberRecord{Workflow: wf, ProjectDir: dir, TokenRequired: true}

	hubCheckMemberToken(m, apitokens.ScopeOperator)
	store := apitokens.Open(apitokens.Path(wf))
	list, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, list, "no remembered token: the check creates none")

	cached, err := hubMemberToken(m, apitokens.ScopeOperator) // the hub caches this secret
	require.NoError(t, err)
	before, ok := apitokens.Open(store.FilePath()).Authenticate(cached)
	require.True(t, ok)

	for range 2 { // and again after another old-format rewrite
		stripTokenKinds(t, store.FilePath())
		stripped, ok := apitokens.Open(store.FilePath()).Authenticate(cached)
		require.True(t, ok)
		require.Empty(t, stripped.Kind)

		hubCheckMemberToken(m, apitokens.ScopeOperator)
		reader := apitokens.Open(store.FilePath())
		tok, ok := reader.Authenticate(cached)
		require.True(t, ok, "the cached secret still authenticates")
		assert.Equal(t, before.ID, tok.ID, "the same token")
		assert.Equal(t, apitokens.KindHub, tok.Kind, "the kind is back without a look-up")
		list, err := reader.List()
		require.NoError(t, err)
		assert.Len(t, list, 1, "nothing created, nothing revoked")
	}

	// A rotated token (the remembered secret no longer matches) is left
	// alone: the check never revokes or replaces.
	_, _, err = store.Rotate(before.ID, "test")
	require.NoError(t, err)
	stripTokenKinds(t, store.FilePath())
	hubCheckMemberToken(m, apitokens.ScopeOperator)
	list, err = apitokens.Open(store.FilePath()).List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Empty(t, list[0].Kind, "a token the hub no longer holds is not re-stamped")
}
