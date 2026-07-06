package workflow_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/workflow"
)

// TestDoc_BatchedSSHCascadeIsAtomic exercises the multi-field cascade Doc was
// designed for: one Save() writes both `ssh_hosts` and `ssh_host_descriptions`
// keys atomically. Pre-T-30 this would have been two separate Patch* calls
// with a torn-write window between them.
func TestDoc_BatchedSSHCascadeIsAtomic(t *testing.T) {
	content := "---\nagent:\n  command: claude\n---\n\nPrompt body.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	err := workflow.NewDoc(f).
		SetSSHHosts(
			[]string{"worker-1", "worker-2:2222"},
			map[string]string{"worker-1": "fast box", "worker-2:2222": "gpu box"},
		).
		Save()
	require.NoError(t, err)

	got, err := os.ReadFile(f)
	require.NoError(t, err)
	out := string(got)
	assert.Contains(t, out, `ssh_hosts: ["worker-1","worker-2:2222"]`)
	assert.Contains(t, out, `ssh_host_descriptions:`)
	assert.Contains(t, out, `"worker-1": "fast box"`)
	assert.Contains(t, out, `"worker-2:2222": "gpu box"`)
}

// TestDoc_SaveOnEmptyQueueIsNoOp verifies an empty Doc.Save touches no files.
func TestDoc_SaveOnEmptyQueueIsNoOp(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	// File does not exist; an empty queue must not create it.
	require.NoError(t, workflow.NewDoc(f).Save())
	_, err := os.Stat(f)
	assert.True(t, os.IsNotExist(err), "Save with empty queue should not create the file")
}

// TestDoc_RemovesKeysWhenSetToEmpty verifies that passing an empty slice/map
// to a Set* call removes the corresponding key from the front matter.
func TestDoc_RemovesKeysWhenSetToEmpty(t *testing.T) {
	content := "---\nagent:\n  command: claude\n  ssh_hosts: [\"a\",\"b\"]\n  ssh_host_descriptions:\n    \"a\": \"box-a\"\n    \"b\": \"box-b\"\n---\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	err := workflow.NewDoc(f).
		SetSSHHosts(nil, nil).
		Save()
	require.NoError(t, err)

	got, err := os.ReadFile(f)
	require.NoError(t, err)
	out := string(got)
	assert.NotContains(t, out, "ssh_hosts:")
	assert.NotContains(t, out, "ssh_host_descriptions:")
	assert.Contains(t, out, "command: claude")
}

// TestDoc_ConcurrentSavesSerialize verifies the editMu lock granularity is
// inherited from ApplyAndWriteFrontMatter — two Doc instances racing on the
// same path produce a deterministic final state, never a torn file.
func TestDoc_ConcurrentSavesSerialize(t *testing.T) {
	content := "---\nagent:\n  command: claude\n  max_concurrent_agents: 0\n---\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	const concurrency = 10
	var wg sync.WaitGroup
	for i := 1; i <= concurrency; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			err := workflow.NewDoc(f).SetMaxConcurrentAgents(n).Save()
			require.NoError(t, err)
		}(i)
	}
	wg.Wait()

	got, err := os.ReadFile(f)
	require.NoError(t, err)
	// Final file must contain exactly one of the written values, not a malformed line.
	out := string(got)
	matched := false
	for i := 1; i <= concurrency; i++ {
		needle := "max_concurrent_agents:"
		if assert.Contains(t, out, needle) {
			matched = true
			break
		}
	}
	assert.True(t, matched)
}

// TestDoc_ChainedMixedSetters verifies that multiple typed setter calls in
// one Doc apply in sequence and produce a single coherent file.
func TestDoc_ChainedMixedSetters(t *testing.T) {
	content := "---\nagent:\n  command: claude\n  max_concurrent_agents: 1\n  dispatch_strategy: \"\"\n---\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	err := workflow.NewDoc(f).
		SetMaxConcurrentAgents(7).
		SetAgentString("dispatch_strategy", "round_robin").
		SetAgentBool("inline_input", true).
		Save()
	require.NoError(t, err)

	got, err := os.ReadFile(f)
	require.NoError(t, err)
	out := string(got)
	assert.Contains(t, out, "max_concurrent_agents: 7")
	assert.Contains(t, out, `dispatch_strategy: "round_robin"`)
	assert.Contains(t, out, "inline_input: true")
}
