package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/workspace"
)

// stackBlocker builds a BlockerRef by value; the package already has a
// blockerRef helper with a different signature.
func stackBlocker(identifier, state string) domain.BlockerRef {
	ref := domain.BlockerRef{Identifier: &identifier}
	if state != "" {
		ref.State = &state
	}
	return ref
}

func stackState() State { return State{TerminalStates: []string{"Done", "Cancelled"}} }

// TestStackedBaseBranchSingleLiveBlocker is the case stacking exists for: work
// that depends on exactly one in-flight change should be reviewable as an
// increment on top of it, not as a diff against main that includes the
// blocker's commits.
func TestStackedBaseBranchSingleLiveBlocker(t *testing.T) {
	issue := domain.Issue{Identifier: "ENG-2", BlockedBy: []domain.BlockerRef{
		stackBlocker("ENG-1", "In Progress"),
	}}
	assert.Equal(t, "itervox/eng-1", stackedBaseBranch(stackState(), issue),
		"the base must be the blocker's branch, derived the same way its own worktree was")
}

// TestStackedBaseBranchDeclinesWhenAmbiguous — with several live blockers any
// choice is arbitrary: the branch would sit on one blocker while still
// depending on the others, so the PR is not reviewable in isolation.
func TestStackedBaseBranchDeclinesWhenAmbiguous(t *testing.T) {
	issue := domain.Issue{Identifier: "ENG-3", BlockedBy: []domain.BlockerRef{
		stackBlocker("ENG-1", "In Progress"),
		stackBlocker("ENG-2", "Todo"),
	}}
	assert.Empty(t, stackedBaseBranch(stackState(), issue),
		"two live blockers have no unambiguous base — fall back to base_branch")
}

// TestStackedBaseBranchIgnoresTerminalBlockers — a landed blocker is already
// in base_branch; stacking on it would only add distance.
func TestStackedBaseBranchIgnoresTerminalBlockers(t *testing.T) {
	issue := domain.Issue{Identifier: "ENG-4", BlockedBy: []domain.BlockerRef{
		stackBlocker("ENG-1", "Done"),
		stackBlocker("ENG-2", "In Progress"),
	}}
	assert.Equal(t, "itervox/eng-2", stackedBaseBranch(stackState(), issue),
		"a terminal blocker is skipped, leaving one live blocker to stack on")

	allDone := domain.Issue{Identifier: "ENG-5", BlockedBy: []domain.BlockerRef{
		stackBlocker("ENG-1", "Done"), stackBlocker("ENG-2", "Cancelled"),
	}}
	assert.Empty(t, stackedBaseBranch(stackState(), allDone),
		"every blocker landed — base_branch already contains their work")
}

func TestStackedBaseBranchEdgeCases(t *testing.T) {
	assert.Empty(t, stackedBaseBranch(stackState(), domain.Issue{Identifier: "ENG-6"}),
		"no blockers: nothing to stack on")

	blank := ""
	assert.Empty(t, stackedBaseBranch(stackState(), domain.Issue{
		Identifier: "ENG-7",
		BlockedBy:  []domain.BlockerRef{{Identifier: &blank}, {Identifier: nil}},
	}), "a blocker with no usable identifier cannot name a branch")

	// A blocker with unknown state is treated as LIVE: assuming it landed
	// would stack on nothing, but assuming it is live at worst falls back
	// when the branch does not exist.
	assert.Equal(t, "itervox/eng-9", stackedBaseBranch(stackState(), domain.Issue{
		Identifier: "ENG-8", BlockedBy: []domain.BlockerRef{stackBlocker("ENG-9", "")},
	}))
}

// TestStackedBaseBranchCountsUnidentifiedBlockersTowardAmbiguity closes a hole
// in the "exactly one live blocker" rule.
//
// An unidentified live blocker used to be skipped BEFORE the ambiguity count,
// so an issue with two live blockers — one of which the tracker reported
// without an identifier — looked like it had exactly one and stacked on it.
// The branch would then sit on one blocker while still depending on another,
// which is precisely what the rule exists to prevent. Linear leaves
// Identifier nil whenever the relation node omits it, so this is reachable.
func TestStackedBaseBranchCountsUnidentifiedBlockersTowardAmbiguity(t *testing.T) {
	unidentified := domain.BlockerRef{State: strPtr("In Progress")}

	twoLive := domain.Issue{Identifier: "ENG-9", BlockedBy: []domain.BlockerRef{
		stackBlocker("ENG-1", "In Progress"),
		unidentified,
	}}
	assert.Empty(t, stackedBaseBranch(stackState(), twoLive),
		"an unidentified live blocker is still a dependency — two live blockers have no unambiguous base")

	// A terminal unidentified blocker is genuinely irrelevant: its work landed.
	oneLive := domain.Issue{Identifier: "ENG-10", BlockedBy: []domain.BlockerRef{
		stackBlocker("ENG-1", "In Progress"),
		{State: strPtr("Done")},
	}}
	assert.Equal(t, "itervox/eng-1", stackedBaseBranch(stackState(), oneLive))

	// A sole live blocker with no identifier cannot name a branch.
	soleUnidentified := domain.Issue{Identifier: "ENG-11", BlockedBy: []domain.BlockerRef{unidentified}}
	assert.Empty(t, stackedBaseBranch(stackState(), soleUnidentified))
}

// stackRecordingProvider records whether the stacked entry point was used and
// with what start point.
type stackRecordingProvider struct {
	plain, stacked int
	startPoint     string
}

func (p *stackRecordingProvider) EnsureWorkspace(_ context.Context, identifier, _ string) (workspace.Workspace, error) {
	p.plain++
	return workspace.Workspace{Path: "/tmp/ws", Identifier: identifier}, nil
}

func (p *stackRecordingProvider) EnsureWorkspaceFrom(_ context.Context, identifier, _, startPoint string) (workspace.Workspace, error) {
	p.stacked++
	p.startPoint = startPoint
	return workspace.Workspace{Path: "/tmp/ws", Identifier: identifier}, nil
}
func (p *stackRecordingProvider) RemoveWorkspace(context.Context, string, string) error { return nil }
func (p *stackRecordingProvider) ResolvePath(string) string                             { return "/tmp/ws" }

// TestStackedPRsGateIsOffByDefault pins invariant 4: no behaviour change for
// anyone who has not opted in.
//
// Nothing previously covered the gate — deleting it entirely left the suite
// green, which meant the opt-in could have been removed by accident and
// silently changed where every worktree branches from.
func TestStackedPRsGateIsOffByDefault(t *testing.T) {
	issue := domain.Issue{Identifier: "ENG-2", BlockedBy: []domain.BlockerRef{
		stackBlocker("ENG-1", "In Progress"),
	}}

	t.Run("disabled uses the plain entry point", func(t *testing.T) {
		p := &stackRecordingProvider{}
		o := &Orchestrator{cfg: &config.Config{}, workspace: p}
		o.storeSnap(State{TerminalStates: []string{"Done"}})

		_, err := o.ensureWorkspaceMaybeStacked(context.Background(), issue, "itervox/eng-2")
		require.NoError(t, err)
		assert.Equal(t, 1, p.plain, "default config must not stack")
		assert.Zero(t, p.stacked)
	})

	t.Run("enabled stacks on the blocker branch", func(t *testing.T) {
		p := &stackRecordingProvider{}
		cfg := &config.Config{}
		cfg.Dependencies.StackedPRs = true
		o := &Orchestrator{cfg: cfg, workspace: p}
		o.storeSnap(State{TerminalStates: []string{"Done"}})

		_, err := o.ensureWorkspaceMaybeStacked(context.Background(), issue, "itervox/eng-2")
		require.NoError(t, err)
		assert.Equal(t, 1, p.stacked)
		assert.Equal(t, "itervox/eng-1", p.startPoint)
	})
}
