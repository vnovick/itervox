package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestPolicyFingerprintKeysAndFiles: the fingerprint names a changed
// effective config key of the run's profile (push, isolation, lanes) as
// "config:<key>", a created, edited or removed governing file by its name,
// and holds hashes only — never a file's text or a config value.
func TestPolicyFingerprintKeysAndFiles(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(wf, []byte("secret-prompt-text"), 0o644))
	cfg := &config.Config{WorkflowPath: wf}
	cfg.Agent.Profiles = map[string]config.AgentProfile{"coder": {Push: config.PushAgent, Command: "claude --token=abc"}}
	o := &Orchestrator{cfg: cfg}

	start := o.runPolicyFingerprint("coder")
	for name, h := range start.files {
		assert.NotContains(t, h, "secret", name)
		if h != policyAbsent {
			assert.Len(t, h, policyHashLen, name)
		}
	}
	assert.Equal(t, policyAbsent, start.files[".itervox/rules.json"], "a missing file is recorded as absent")
	for k, v := range start.keys {
		assert.NotContains(t, v, "abc", k)
	}

	p := cfg.Agent.Profiles["coder"]
	p.Push, p.Isolation = config.PushDaemon, config.IsolationProcess
	cfg.Agent.Profiles["coder"] = p
	cfg.Guardrails.Lanes.Human = []string{".github/**"}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".itervox", "rules.json"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(wf, []byte("edited"), 0o644))

	got := policyChanges(start, o.runPolicyFingerprint("coder"))
	assert.Equal(t, []string{".itervox/rules.json", "WORKFLOW.md", "config:agent.isolation", "config:agent.push", "config:guardrails.lanes"}, got)
	assert.Empty(t, policyChanges(start, start))
	assert.True(t, (&Orchestrator{cfg: &config.Config{}}).runPolicyFingerprint("x").empty(), "no workflow: nothing recorded")
	assert.Nil(t, o.runPolicyChanged(&runHandle{}), "a run without a fingerprint reports nothing")
}

// TestPolicyFeedPayloadIsBounded: a policy_changed payload keeps at most
// maxPolicyNames names, each capped like every feed label.
func TestPolicyFeedPayloadIsBounded(t *testing.T) {
	files := make([]string, 100)
	for i := range files {
		files[i] = "f"
	}
	ev := sanitizeFeedEvent(FeedEvent{Kind: FeedPolicyChanged, Payload: FeedPayload{Files: files, RunIDs: files}})
	assert.Len(t, ev.Payload.Files, maxPolicyNames)
	assert.Len(t, ev.Payload.RunIDs, maxPolicyNames)
}
