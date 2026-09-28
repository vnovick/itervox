package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/workflow"
)

// TestPatchRootGitignoreIgnoresWorkflowLock (CORE-149): the inter-process
// edit lock's sidecar is transient runtime state. `itervox init` /
// `init --update` must ignore exactly the file the workflow package locks,
// and only once however often the patch runs.
func TestPatchRootGitignoreIgnoresWorkflowLock(t *testing.T) {
	dir := t.TempDir()
	require.Equal(t, workflowLockGitignoreEntry,
		filepath.Base(workflow.LockFilePath(filepath.Join(dir, "WORKFLOW.md"))),
		"the .gitignore entry must name the sidecar the workflow package actually locks")

	gi := filepath.Join(dir, ".gitignore")
	require.NoError(t, os.WriteFile(gi, []byte("node_modules/\n"), 0o644))
	require.NoError(t, patchRootGitignoreForAgents(dir))
	require.NoError(t, patchRootGitignoreForAgents(dir))
	data, err := os.ReadFile(gi)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(data), "\n"+workflowLockGitignoreEntry+"\n"),
		".gitignore:\n%s", data)
}

// TestRewriteServerPortSerializesWithLockedPatchers (CORE-149): the
// `itervox init --update --server-port` writer must wait for a locked
// patcher that is mid read-modify-write instead of overwriting its edit.
func TestRewriteServerPortSerializesWithLockedPatchers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte("---\nitervox_schema_version: 2\ntracker:\n  kind: memory\nagent:\n  max_concurrent_agents: 3\nserver:\n  port: 8090\n---\nbody\n"), 0o644))

	holding, release := make(chan struct{}), make(chan struct{})
	partnerDone := make(chan error, 1)
	go func() {
		partnerDone <- workflow.ApplyAndWriteFrontMatter(path,
			func(front []string) ([]string, error) {
				close(holding)
				<-release
				return front, nil
			},
			workflow.MutateAgentIntField("max_concurrent_agents", 9))
	}()
	<-holding

	rewriteDone := make(chan error, 1)
	go func() { rewriteDone <- rewriteServerPort(path, 0) }()
	select {
	case err := <-rewriteDone:
		t.Fatalf("rewriteServerPort finished while a locked patcher held WORKFLOW.md (err=%v) — it bypasses the edit lock", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-partnerDone)
	require.NoError(t, <-rewriteDone)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "max_concurrent_agents: 9", "the patcher's edit was lost:\n%s", data)
	require.Contains(t, string(data), "port: 0", "the server.port rewrite was lost:\n%s", data)
}
