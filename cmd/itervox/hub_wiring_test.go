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
