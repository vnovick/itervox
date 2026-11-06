package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// TestTaskInstructionsInPromptAndDigest (#251): a task's custom
// instructions are a `## Task instructions` block right after the playbook
// (which follows INSTRUCTIONS.md), and a prompt-digest component only when
// present: a run of an issue that is not a task keeps the digest it had,
// different instructions give a different version, the history records the
// part, and a reviewer run is not given the block. The first run freezes
// the instructions.
func TestTaskInstructionsInPromptAndDigest(t *testing.T) {
	f := newProvenanceFixture(t)
	enablePlaybooks(t, f.cfg, map[string]string{"bug-fix": bugFixPlaybook})
	f.issue.Labels = []string{"bug"}
	before, partsBefore := f.digestFor(t, "")
	assert.Empty(t, partsBefore.Task)

	// A task recorded for another issue changes nothing for this one.
	f.o.recordTask("ENG-9", TaskSpec{Instructions: "not mine", CreatedAt: time.Now()})
	same, parts := f.digestFor(t, "")
	assert.Equal(t, before, same, "a run that is not a task keeps its digest")
	assert.Equal(t, partsBefore, parts)

	// ENG-1 becomes a task: its run carries the block and a new version.
	f.o.recordTask("ENG-1", TaskSpec{Instructions: "TASK-MARK cover the error paths.", CreatedAt: time.Now()})
	entry := &RunEntry{Issue: f.issue, ProfileName: "implementer", Backend: "claude", StartedAt: time.Now()}
	ctx := f.o.startRun(t.Context(), &f.state, entry)
	require.NotEmpty(t, entry.PromptParts.Task, "the instructions are a digest component")
	assert.NotEqual(t, before, entry.PromptDigest)
	assert.Equal(t, partsBefore.Playbook, entry.PromptParts.Playbook)
	assert.Equal(t, partsBefore.Instructions, entry.PromptParts.Instructions)
	assert.Equal(t, partsBefore.Template, entry.PromptParts.Template)
	spec, block := runTask(ctx)
	assert.Equal(t, "## Task instructions\n\nTASK-MARK cover the error paths.", block)
	require.NotNil(t, spec.StartedAt, "the first run freezes the instructions")
	assert.ErrorIs(t, f.o.SetTaskInstructions("ENG-1", "changed"), ErrTaskStarted)

	// Prompt order: INSTRUCTIONS.md, then the playbook, then the task.
	h := runFrom(ctx)
	text := profilePromptTail(profileTailInput{
		Playbook: h.playbook.Playbook.Block(), TaskInstructions: h.taskBlock,
		Template: f.cfg.PromptTemplate, ProfileName: "implementer", Profiles: f.cfg.Agent.Profiles,
		Issue: f.issue, RunVars: map[string]any{}, Backend: "claude",
	})
	instr, pb, task := strings.Index(text, "INSTRUCTIONS-MARK"), strings.Index(text, "## Playbook: bug-fix"), strings.Index(text, "## Task instructions")
	require.True(t, instr >= 0 && pb > instr && task > pb,
		"order must be INSTRUCTIONS → playbook → task (instructions %d, playbook %d, task %d)", instr, pb, task)
	assert.Contains(t, text[task:], "TASK-MARK")

	// Other instructions are another prompt version.
	c := profilePromptComponents(f.cfg, "implementer", false, h.rules, h.memory, h.playbook.Playbook.Block())
	c.task = "## Task instructions\n\nsomething else"
	other, otherParts := c.digest()
	assert.NotEqual(t, entry.PromptDigest, other)
	assert.NotEqual(t, entry.PromptParts.Task, otherParts.Task)

	// A reviewer of a task gets no block (and no digest part).
	f.o.recordTask("ENG-2", TaskSpec{Instructions: "review me", CreatedAt: time.Now()})
	revIssue := f.issue
	revIssue.ID, revIssue.Identifier = "id2", "ENG-2"
	rev := &RunEntry{Issue: revIssue, ProfileName: "implementer", Backend: "claude", Kind: "reviewer", StartedAt: time.Now()}
	revCtx := f.o.startRun(t.Context(), &f.state, rev)
	assert.Empty(t, rev.PromptParts.Task)
	_, revBlock := runTask(revCtx)
	assert.Empty(t, revBlock)

	// The history records the part.
	f.state.Running[f.issue.ID] = entry
	f.state.Claimed[f.issue.ID] = struct{}{}
	run := f.exit(t, entry)
	assert.Equal(t, entry.PromptDigest, run.PromptDigest)
	assert.Equal(t, entry.PromptParts.Task, run.PromptParts.Task)
}

// TestTaskBaseBranchStartsTheWorktree (#251): a task's base branch is the
// worktree's start point (stacking off or on), and the prompt names it as
// the pull request's base.
func TestTaskBaseBranchStartsTheWorktree(t *testing.T) {
	p := &stackRecordingProvider{}
	o := &Orchestrator{cfg: &config.Config{}, workspace: p}
	o.storeSnap(State{TerminalStates: []string{"Done"}})
	_, err := o.ensureWorkspaceMaybeStacked(t.Context(), domain.Issue{Identifier: "TASK-1"}, "itervox/task-1", "release/2.x")
	require.NoError(t, err)
	assert.Equal(t, 1, p.stacked)
	assert.Equal(t, "release/2.x", p.startPoint)
	assert.Contains(t, buildTaskBaseBlock("release/2.x"), "gh pr create --base release/2.x")
	assert.Empty(t, buildTaskBaseBlock(""))
}

// TestTaskCreateHoldsSameTitleUntilRecorded (#251): while a task is being
// created, a tick skips an issue with its title, so no run can start before
// the task's instructions are recorded; the hold ends with the create.
func TestTaskCreateHoldsSameTitleUntilRecorded(t *testing.T) {
	o := &Orchestrator{}
	assert.False(t, o.taskCreationPending("Upgrade lodash"))
	end := o.beginTaskCreate("Upgrade lodash")
	end2 := o.beginTaskCreate("Upgrade lodash")
	assert.True(t, o.taskCreationPending(" Upgrade lodash "))
	assert.False(t, o.taskCreationPending("Something else"))
	end()
	assert.True(t, o.taskCreationPending("Upgrade lodash"), "a second create of the same title still holds it")
	end2()
	assert.False(t, o.taskCreationPending("Upgrade lodash"))
}
