package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/memory"
	"github.com/vnovick/itervox/internal/tracker"
)

// memoryOn is the memory config Load gives a workflow without a memory:
// block.
var memoryOn = config.MemoryConfig{
	Enabled: true, TokenBudget: config.DefaultMemoryTokenBudget, PromptShareTokens: config.DefaultMemoryPromptShareTokens,
}

// writeMemory writes a memory file with the given entries (JSON objects).
func writeMemory(t *testing.T, path string, entries ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	body := `{"memories":[` + strings.Join(entries, ",") + `]}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

const (
	memPnpm     = `{"id":"pnpm","type":"fact","status":"active","text":"MEM-PNPM this repo uses pnpm."}`
	memPinned   = `{"id":"pr-size","type":"preference","status":"active","pinned":true,"text":"MEM-PINNED keep PRs small."}`
	memProposed = `{"id":"draft","type":"lesson","status":"proposed","text":"MEM-PROPOSED not approved yet."}`
)

// memoryProject lays out rules (project layer) plus a private and a project
// memory file and returns the workflow path.
func memoryProject(t *testing.T) string {
	t.Helper()
	wf := rulesProject(t, "", projectRules, "")
	private := filepath.Join(filepath.Dir(filepath.Dir(wf)), "home", "memory.json")
	t.Setenv(memory.UserMemoryEnv, private)
	writeMemory(t, private, memPinned)
	writeMemory(t, memory.ProjectPath(wf), memPnpm, memProposed)
	return wf
}

// TestPromptAssemblyOrderRulesMemorySoul (#202): a worker's prompt carries
// the profile tail in the order rules → memory → SOUL.md →
// INSTRUCTIONS.md, after the WORKFLOW.md template body and the daemon's
// blocks. Proposed entries are not used, and memory.enabled: false leaves
// the block out.
func TestPromptAssemblyOrderRulesMemorySoul(t *testing.T) {
	wf := memoryProject(t)
	cfg := automationBaseCfg()
	cfg.WorkflowPath = wf
	cfg.PromptTemplate = "MAIN {{ issue.identifier }}"
	cfg.Agent.MaxTurns = 1
	cfg.Memory = memoryOn
	cfg.Agent.Profiles["implementer"] = implementerProfile
	p := renderWorkerPrompt(t, cfg, "implementer")

	at := func(s string) int {
		i := strings.Index(p, s)
		require.GreaterOrEqual(t, i, 0, "%q missing from prompt:\n%s", s, p)
		return i
	}
	main, rulesAt, memAt := at("MAIN ENG-1"), at("## Rules"), at("## Memory")
	pinned, pnpm := at("MEM-PINNED"), at("MEM-PNPM")
	soul, instructions := at("SOUL-MARK"), at("INSTRUCTIONS-MARK")
	assert.Less(t, main, rulesAt, "the template body comes first")
	assert.Less(t, rulesAt, memAt, "rules before memory")
	assert.Less(t, memAt, pinned)
	assert.Less(t, pinned, pnpm, "pinned entries lead the memory block")
	assert.Less(t, pnpm, soul, "memory before SOUL.md")
	assert.Less(t, soul, instructions, "SOUL.md before INSTRUCTIONS.md")
	assert.Equal(t, 1, strings.Count(p, "## Memory"))
	assert.NotContains(t, p, "MEM-PROPOSED", "a proposed entry waits for approval")

	cfg.Memory.Enabled = false
	off := renderWorkerPrompt(t, cfg, "implementer")
	assert.NotContains(t, off, "## Memory")
	assert.Contains(t, off, "## Rules")
}

// renderWorkerPrompt runs the real runWorker once under a dispatched run
// (startRun) and returns the first turn's prompt.
func renderWorkerPrompt(t *testing.T, cfg *config.Config, profile string) string {
	t.Helper()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Build the web app", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &promptCapturingRunner{}
	o := New(cfg, mt, runner, &stubWorkspaceProvider{path: t.TempDir()})
	o.events = make(chan OrchestratorEvent, 64)
	state := NewState(cfg)
	entry := &RunEntry{Issue: issue, ProfileName: profile, Backend: "claude", StartedAt: time.Now()}
	ctx := o.startRun(t.Context(), &state, entry)
	o.workersWg.Add(issue.Identifier)
	o.runWorker(ctx, issue, 0, "", "claude", "claude", profile, true, nil, nil, nil)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	require.NotEmpty(t, runner.prompts)
	return runner.prompts[0]
}

// TestMemoryUsedIDsRecordedOnCompletedRun (#202): startRun records the ids
// of the memory a run is given on its RunEntry, its exit writes them onto
// the CompletedRun, and the worker's EventMemoryUsed — handled by the event
// loop — bumps those entries' lastUsed/useCount in the files (the write
// runs on a goroutine Run joins, never on the loop). The memory is part of
// the prompt digest: changing an entry's text changes the version.
func TestMemoryUsedIDsRecordedOnCompletedRun(t *testing.T) {
	wf := memoryProject(t)
	cfg := automationBaseCfg()
	cfg.WorkflowPath = wf
	cfg.Agent.MaxTurns = 1
	cfg.Memory = memoryOn
	cfg.Agent.Profiles["implementer"] = implementerProfile
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "pnpm build", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &promptCapturingRunner{}
	o := New(cfg, mt, runner, &stubWorkspaceProvider{path: t.TempDir()})
	o.events = make(chan OrchestratorEvent, 64)
	state := NewState(cfg)

	entry := &RunEntry{Issue: issue, ProfileName: "implementer", Backend: "claude", StartedAt: time.Now()}
	ctx := o.startRun(t.Context(), &state, entry)
	assert.Equal(t, []string{"pr-size", "pnpm"}, entry.MemoryIDs, "pinned first, proposed left out")
	assert.NotEmpty(t, entry.PromptParts.Memory, "the memory is a digest component")
	state.Running[issue.ID] = entry

	o.workersWg.Add(issue.Identifier)
	o.runWorker(ctx, issue, 0, "", "claude", "claude", "implementer", true, nil, nil, nil)
	var used *OrchestratorEvent
	for len(o.events) > 0 {
		ev := <-o.events
		if ev.Type == EventMemoryUsed {
			require.Nil(t, used, "memory use is reported once per run")
			used = &ev
		}
	}
	require.NotNil(t, used, "the worker reports the memory its prompt carried")
	assert.Equal(t, entry.RunID, used.RunID)
	state = o.handleEvent(t.Context(), state, *used)
	o.memoryWg.Wait()

	files := memory.ReadAll(wf)
	counts := map[string]int{}
	for _, f := range files {
		require.NoError(t, f.Err)
		for _, e := range f.Entries {
			counts[e.ID] = e.UseCount
			if e.UseCount > 0 {
				require.NotNil(t, e.LastUsed, e.ID)
			}
		}
	}
	assert.Equal(t, map[string]int{"pr-size": 1, "pnpm": 1, "draft": 0}, counts)

	o.recordHistory(entry, issue, time.Now(), "succeeded")
	hist := o.RunHistory()
	require.NotEmpty(t, hist)
	assert.Equal(t, []string{"pr-size", "pnpm"}, hist[len(hist)-1].MemoryIDs)

	// The bumped counters do not change the version; an edited text does.
	again := &RunEntry{Issue: issue, ProfileName: "implementer", Backend: "claude", StartedAt: time.Now()}
	_ = o.startRun(t.Context(), &state, again)
	assert.Equal(t, entry.PromptDigest, again.PromptDigest)
	writeMemory(t, memory.ProjectPath(wf), strings.Replace(memPnpm, "uses pnpm", "uses pnpm 9", 1))
	edited := &RunEntry{Issue: issue, ProfileName: "implementer", Backend: "claude", StartedAt: time.Now()}
	_ = o.startRun(t.Context(), &state, edited)
	assert.NotEqual(t, entry.PromptDigest, edited.PromptDigest)
	assert.NotEqual(t, entry.PromptParts.Memory, edited.PromptParts.Memory)
	assert.Equal(t, entry.PromptParts.Rules, edited.PromptParts.Rules)
}

// TestMemoryBudgetSharesThePromptWithRules (#202): memory gets what the
// rules leave of the rules-plus-memory share, up to token_budget, and the
// eval composer selects the memory a dispatch would, with the same digest.
func TestMemoryBudgetSharesThePromptWithRules(t *testing.T) {
	wf := memoryProject(t)
	cfg := automationBaseCfg()
	cfg.WorkflowPath = wf
	cfg.PromptTemplate = "T {{ issue.identifier }}"
	cfg.Agent.Profiles["implementer"] = implementerProfile
	cfg.Memory = memoryOn
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "pnpm"}
	set := resolveRulesFor(cfg, "implementer", "claude")
	rulesTokens := memory.ApproxTokens(set.Block())

	full, _ := resolveMemoryFor(cfg, "implementer", issue, set)
	assert.Equal(t, []string{"pr-size", "pnpm"}, full.IDs())

	cfg.Memory.PromptShareTokens = rulesTokens + 1 // rules leave memory 1 token
	tight, _ := resolveMemoryFor(cfg, "implementer", issue, set)
	assert.Equal(t, []string{"pr-size"}, tight.IDs(), "only the pinned entry survives")
	assert.Equal(t, 1, tight.Dropped)

	cfg.Memory = memoryOn
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	entry := &RunEntry{Issue: issue, ProfileName: "implementer", Backend: "claude", StartedAt: time.Now()}
	_ = o.startRun(t.Context(), &state, entry)
	composed, err := ComposeEvalPrompt(EvalPromptInput{Config: cfg, ProfileName: "implementer", Backend: "claude", Issue: issue})
	require.NoError(t, err)
	assert.Equal(t, entry.MemoryIDs, composed.Memory.IDs())
	assert.Equal(t, entry.PromptDigest, composed.Digest)
	assert.Less(t, strings.Index(composed.Text, "## Memory"), strings.Index(composed.Text, "SOUL-MARK"))
}
