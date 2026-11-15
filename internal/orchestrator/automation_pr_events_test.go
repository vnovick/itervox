package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

const testPRURL = "https://github.com/acme/app/pull/42"

// prEventsHarness returns an orchestrator with the given PR-event rules and
// a State with no free slot, so an accepted dispatch lands in the
// automation queue where its trigger can be inspected without a worker.
func prEventsHarness(t *testing.T, rules ...PREventAutomation) (*Orchestrator, State, domain.Issue) {
	t.Helper()
	cfg := automationBaseCfg()
	cfg.Agent.Profiles["fixer"] = config.AgentProfile{Command: "claude"}
	o := newOrchestratorForTest(cfg)
	o.SetPREventAutomations(rules)
	state := NewState(cfg)
	state.MaxConcurrentAgents = 0
	return o, state, automationIssue("In Review")
}

// managePR records testPRURL for issue the way a finished run's exit does.
func managePR(state *State, issue domain.Issue, since time.Time, headSHA string) {
	recordManagedPR(state, &RunEntry{StartedAt: since},
		&RunEntry{Issue: issue, PRURL: testPRURL, HeadSHA: headSHA, BranchName: "itervox/eng-1"}, since.Add(time.Minute))
}

// deliverPREvent runs ev through the producer entry point and every event it
// sends through the event loop's handler, and returns how many automation
// dispatch events the producer sent.
func deliverPREvent(t *testing.T, o *Orchestrator, state *State, issue domain.Issue, ev PREvent) int {
	t.Helper()
	n := o.DispatchPREventAutomations(t.Context(), issue, ev)
	for range n {
		select {
		case e := <-o.events:
			require.Equal(t, EventDispatchAutomation, e.Type)
			*state = o.handleEvent(t.Context(), *state, e)
		case <-time.After(time.Second):
			t.Fatal("dispatch event not sent")
		}
	}
	return n
}

func queuedTriggers(state State) []AutomationTriggerContext {
	var out []AutomationTriggerContext
	for _, e := range sortedAutomationQueue(state) {
		out = append(out, e.Trigger)
	}
	return out
}

func checkEvent(sha, name, conclusion string, at time.Time) PREvent {
	return PREvent{
		TriggerType: config.AutomationTriggerPRCheckCompleted, PRURL: testPRURL, PRNumber: 42,
		Branch: "itervox/eng-1", HeadSHA: sha, CheckName: name, CheckConclusion: conclusion,
		CheckCompletedAt: at, MergeableState: "unstable", Source: "poll",
	}
}

// TestPRCheckCompletedDispatchesManagedPRWithVars (#173): a completed check
// on a managed PR dispatches the matching rule, and the trigger carries every
// documented variable.
func TestPRCheckCompletedDispatchesManagedPRWithVars(t *testing.T) {
	o, state, issue := prEventsHarness(t, PREventAutomation{
		ID: "fix-ci", TriggerType: config.AutomationTriggerPRCheckCompleted, ProfileName: "fixer",
		CheckNames: []string{"CI"}, Conclusions: []string{"failure"},
	})
	since := time.Now().Add(-time.Hour)
	managePR(&state, issue, since, "aaa")

	assert.Equal(t, 0, deliverPREvent(t, o, &state, issue, checkEvent("aaa", "ci", "success", time.Now())),
		"conclusion filter: a green check does not match")
	assert.Equal(t, 0, deliverPREvent(t, o, &state, issue, checkEvent("aaa", "lint", "failure", time.Now())),
		"check_names filter: another check does not match")
	require.Equal(t, 1, deliverPREvent(t, o, &state, issue, checkEvent("aaa", "ci", "FAILURE", time.Now())))

	triggers := queuedTriggers(state)
	require.Len(t, triggers, 1)
	assert.Equal(t, uint64(1), state.AutomationDispatchesPREventTotal)
	vars := automationTriggerBindings(&AutomationDispatch{Trigger: triggers[0]})["trigger"].(map[string]any)
	assert.Equal(t, config.AutomationTriggerPRCheckCompleted, vars["type"])
	assert.Equal(t, testPRURL, vars["pr_url"])
	assert.Equal(t, 42, vars["pr_number"])
	assert.Equal(t, "itervox/eng-1", vars["pr_branch"])
	assert.Equal(t, "aaa", vars["head_sha"])
	assert.Equal(t, "ci", vars["check_name"])
	assert.Equal(t, "failure", vars["check_conclusion"])
	assert.Equal(t, "unstable", vars["mergeable_state"])
	assert.Equal(t, "poll", vars["event_source"])
}

// TestPRCheckSameKeyNeverDispatchesTwice (#173): the same (pr, sha, check,
// conclusion) dispatches once — a re-delivered webhook or a repeated poll is
// dropped — while a new SHA or a new conclusion is a new event.
func TestPRCheckSameKeyNeverDispatchesTwice(t *testing.T) {
	o, state, issue := prEventsHarness(t, PREventAutomation{
		ID: "fix-ci", TriggerType: config.AutomationTriggerPRCheckCompleted, ProfileName: "fixer",
	})
	managePR(&state, issue, time.Now().Add(-time.Hour), "aaa")
	at := time.Now()

	deliverPREvent(t, o, &state, issue, checkEvent("aaa", "ci", "failure", at))
	deliverPREvent(t, o, &state, issue, checkEvent("aaa", "ci", "failure", at))
	deliverPREvent(t, o, &state, issue, checkEvent("aaa", "CI", "failure", at.Add(time.Minute)))
	assert.Equal(t, uint64(1), state.AutomationDispatchesPREventTotal)
	assert.Equal(t, uint64(2), state.AutomationDroppedPREventDedupTotal)

	deliverPREvent(t, o, &state, issue, checkEvent("bbb", "ci", "failure", at))
	deliverPREvent(t, o, &state, issue, checkEvent("bbb", "ci", "success", at))
	assert.Equal(t, uint64(3), state.AutomationDispatchesPREventTotal)
	assert.Len(t, queuedTriggers(state), 3, "one queue entry per event")
}

// TestPREventDedupSurvivesRestart (#173): the managed PRs and the dedup
// ledger persist, so after a restart the same check does not dispatch again.
func TestPREventDedupSurvivesRestart(t *testing.T) {
	rule := PREventAutomation{ID: "fix-ci", TriggerType: config.AutomationTriggerPRCheckCompleted, ProfileName: "fixer"}
	o, state, issue := prEventsHarness(t, rule)
	path := filepath.Join(t.TempDir(), "pr_events.json")
	o.SetPREventsFile(path)
	managePR(&state, issue, time.Now().Add(-time.Hour), "aaa")
	at := time.Now()
	deliverPREvent(t, o, &state, issue, checkEvent("aaa", "ci", "failure", at))
	require.Equal(t, uint64(1), state.AutomationDispatchesPREventTotal)
	o.savePREventsToDisk(state.Clone())

	o2, fresh, _ := prEventsHarness(t, rule)
	o2.SetPREventsFile(path)
	fresh = o2.loadPREventsFromDisk(fresh)
	require.Contains(t, fresh.ManagedPRs, testPRURL, "managed PRs are restored")
	deliverPREvent(t, o2, &fresh, issue, checkEvent("aaa", "ci", "failure", at))
	assert.Equal(t, uint64(0), fresh.AutomationDispatchesPREventTotal)
	assert.Equal(t, uint64(1), fresh.AutomationDroppedPREventDedupTotal)
}

// TestPREventUnmanagedPRNeverDispatches (#173): a PR no itervox run recorded
// — or one recorded for another issue — never dispatches.
func TestPREventUnmanagedPRNeverDispatches(t *testing.T) {
	o, state, issue := prEventsHarness(t,
		PREventAutomation{ID: "fix-ci", TriggerType: config.AutomationTriggerPRCheckCompleted, ProfileName: "fixer"},
		PREventAutomation{ID: "on-review", TriggerType: config.AutomationTriggerPRReviewSubmitted, ProfileName: "fixer"},
	)
	deliverPREvent(t, o, &state, issue, checkEvent("aaa", "ci", "failure", time.Now()))
	deliverPREvent(t, o, &state, issue, PREvent{TriggerType: config.AutomationTriggerPRReviewSubmitted, PRURL: testPRURL,
		ReviewID: "1", ReviewAuthor: "greptile-apps[bot]", ReviewState: "commented", ReviewSubmittedAt: time.Now()})

	other := domain.Issue{ID: "id2", Identifier: "ENG-2", State: "In Review"}
	managePR(&state, other, time.Now().Add(-time.Hour), "aaa")
	deliverPREvent(t, o, &state, issue, checkEvent("aaa", "ci", "failure", time.Now()))

	assert.Equal(t, uint64(0), state.AutomationDispatchesPREventTotal)
	assert.Equal(t, uint64(3), state.AutomationDroppedPREventUnmanagedTotal)
	assert.Empty(t, state.AutomationQueue)
}

// TestPRSynchronizedSecondPushDispatchesAndPROpenedStaysOnce (#173): the PR
// is recorded through a real worker exit; a second push dispatches
// pr_synchronized once, the first head never does, and pr_opened still fires
// once per PR however often the PR is re-detected.
func TestPRSynchronizedSecondPushDispatchesAndPROpenedStaysOnce(t *testing.T) {
	o, state, issue := prEventsHarness(t, PREventAutomation{
		ID: "on-push", TriggerType: config.AutomationTriggerPRSynchronized, ProfileName: "fixer",
	})
	o.DryRun = true
	o.issueProfiles = map[string]string{}
	o.issueBackends = map[string]string{}
	state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now().Add(-time.Minute)}
	attempt := 0
	state = o.handleEvent(t.Context(), state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: issue.ID,
		RunEntry: &RunEntry{Issue: issue, PRURL: testPRURL, HeadSHA: "first", BranchName: "itervox/eng-1",
			TerminalReason: TerminalSucceeded, RetryAttempt: &attempt},
	})
	require.Contains(t, state.ManagedPRs, testPRURL, "the run's exit records the PR")
	assert.Equal(t, "first", state.ManagedPRs[testPRURL].OpenedHeadSHA)

	push := func(sha string) {
		deliverPREvent(t, o, &state, issue, PREvent{TriggerType: config.AutomationTriggerPRSynchronized,
			PRURL: testPRURL, PRNumber: 42, Branch: "itervox/eng-1", HeadSHA: sha, Source: "webhook"})
	}
	push("first")
	assert.Equal(t, uint64(0), state.AutomationDispatchesPREventTotal, "the opening head is not a new push")
	push("second")
	push("second")
	assert.Equal(t, uint64(1), state.AutomationDispatchesPREventTotal, "a second push dispatches once")
	triggers := queuedTriggers(state)
	require.Len(t, triggers, 1)
	assert.Equal(t, "second", triggers[0].HeadSHA)
	assert.Equal(t, config.AutomationTriggerPRSynchronized, triggers[0].Type)

	// pr_opened stays once per PR (claimPROpenedDedup), even as the PR keeps
	// being re-detected by later runs.
	opened := AutomationDispatch{AutomationID: "review-pr", ProfileName: "fixer",
		Trigger: AutomationTriggerContext{Type: config.AutomationTriggerPROpened, PRURL: testPRURL}}
	for range 3 {
		state = o.handleEvent(t.Context(), state, OrchestratorEvent{Type: EventDispatchAutomation, Issue: &issue, Automation: &opened})
	}
	assert.Equal(t, uint64(1), state.AutomationDispatchesPROpenedTotal)
	assert.Equal(t, uint64(2), state.AutomationDroppedPROpenedDedupTotal)
}

// TestPRSynchronizedOwnPushDoesNotRefire (#173): a run that a pr_synchronized
// rule dispatched advances that rule's baseline to the head it pushed, so the
// rule does not fire on its own push (and a queued copy is dropped), while
// another rule still sees the push.
func TestPRSynchronizedOwnPushDoesNotRefire(t *testing.T) {
	o, state, issue := prEventsHarness(t,
		PREventAutomation{ID: "on-push", TriggerType: config.AutomationTriggerPRSynchronized, ProfileName: "fixer"},
		PREventAutomation{ID: "other", TriggerType: config.AutomationTriggerPRSynchronized, ProfileName: "fixer"},
	)
	managePR(&state, issue, time.Now().Add(-time.Hour), "first")
	push := PREvent{TriggerType: config.AutomationTriggerPRSynchronized, PRURL: testPRURL, HeadSHA: "fix"}
	// The push is seen while the on-push run is still going: both queue.
	deliverPREvent(t, o, &state, issue, push)
	require.Len(t, queuedTriggers(state), 2)

	recordManagedPR(&state,
		&RunEntry{StartedAt: time.Now(), AutomationID: "on-push", TriggerType: config.AutomationTriggerPRSynchronized},
		&RunEntry{Issue: issue, PRURL: testPRURL, HeadSHA: "fix"}, time.Now())
	entries := sortedAutomationQueue(state)
	require.Len(t, entries, 1, "the rule's own push is dropped from the queue")
	assert.Equal(t, "other", entries[0].AutomationID)
	assert.Equal(t, uint64(1), state.AutomationDropsSelfReentryTotal)
}

// TestPRReviewSubmittedDispatchesWithAuthorAndState (#173).
func TestPRReviewSubmittedDispatchesWithAuthorAndState(t *testing.T) {
	o, state, issue := prEventsHarness(t, PREventAutomation{
		ID: "greptile", TriggerType: config.AutomationTriggerPRReviewSubmitted, ProfileName: "fixer",
		ReviewAuthors: []string{"greptile-apps[bot]"},
	})
	managePR(&state, issue, time.Now().Add(-time.Hour), "aaa")
	review := func(id, author string) PREvent {
		return PREvent{TriggerType: config.AutomationTriggerPRReviewSubmitted, PRURL: testPRURL, HeadSHA: "aaa",
			ReviewID: id, ReviewAuthor: author, ReviewState: "COMMENTED", ReviewSubmittedAt: time.Now()}
	}
	assert.Equal(t, 0, deliverPREvent(t, o, &state, issue, review("1", "octocat")))
	deliverPREvent(t, o, &state, issue, review("2", "Greptile-Apps[bot]"))
	deliverPREvent(t, o, &state, issue, review("2", "greptile-apps[bot]"))
	assert.Equal(t, uint64(1), state.AutomationDispatchesPREventTotal, "once per review id")
	vars := automationTriggerBindings(&AutomationDispatch{Trigger: queuedTriggers(state)[0]})["trigger"].(map[string]any)
	assert.Equal(t, "Greptile-Apps[bot]", vars["review_author"])
	assert.Equal(t, "commented", vars["review_state"])
}

// TestPREventsBeforeManagementIgnored (#173): checks and reviews that
// predate the run that recorded the PR are not dispatched.
func TestPREventsBeforeManagementIgnored(t *testing.T) {
	o, state, issue := prEventsHarness(t,
		PREventAutomation{ID: "fix-ci", TriggerType: config.AutomationTriggerPRCheckCompleted, ProfileName: "fixer"},
	)
	since := time.Now()
	managePR(&state, issue, since, "aaa")
	deliverPREvent(t, o, &state, issue, checkEvent("aaa", "ci", "failure", since.Add(-time.Minute)))
	assert.Equal(t, uint64(0), state.AutomationDispatchesPREventTotal)
	deliverPREvent(t, o, &state, issue, checkEvent("aaa", "ci", "failure", since.Add(time.Minute)))
	assert.Equal(t, uint64(1), state.AutomationDispatchesPREventTotal)
}

// TestPRMergeableChangedFiresOnConflict (#173): without a filter only a move
// into or out of "dirty" fires, with the previous state bound.
func TestPRMergeableChangedFiresOnConflict(t *testing.T) {
	o, state, issue := prEventsHarness(t, PREventAutomation{
		ID: "conflict", TriggerType: config.AutomationTriggerPRMergeableChanged, ProfileName: "fixer",
	})
	managePR(&state, issue, time.Now().Add(-time.Hour), "aaa")
	move := func(ms string) {
		deliverPREvent(t, o, &state, issue, PREvent{TriggerType: config.AutomationTriggerPRMergeableChanged,
			PRURL: testPRURL, HeadSHA: "aaa", MergeableState: ms})
	}
	move("unknown")
	move("clean")
	move("blocked")
	assert.Equal(t, uint64(0), state.AutomationDispatchesPREventTotal, "no conflict yet")
	move("dirty")
	require.Equal(t, uint64(1), state.AutomationDispatchesPREventTotal)
	assert.Equal(t, "blocked", queuedTriggers(state)[0].PreviousMergeableState)
	assert.Equal(t, "dirty", queuedTriggers(state)[0].MergeableState)
	move("dirty")
	move("clean")
	assert.Equal(t, uint64(2), state.AutomationDispatchesPREventTotal, "the conflict clearing fires too")
}

func TestPRMergeableFiresTable(t *testing.T) {
	for _, tc := range []struct {
		prev, cur string
		filter    []string
		want      bool
	}{
		{"", "clean", nil, false},
		{"", "dirty", nil, true},
		{"clean", "dirty", nil, true},
		{"dirty", "clean", nil, true},
		{"clean", "blocked", nil, false},
		{"dirty", "clean", []string{"dirty"}, false},
		{"clean", "behind", []string{"behind"}, true},
		{"", "behind", []string{"behind"}, false},
		{"dirty", "dirty", nil, false},
	} {
		assert.Equal(t, tc.want, prEventMergeableFires(tc.prev, tc.cur, tc.filter), "%+v", tc)
	}
}

// TestPruneManagedPRsOnTerminal (#173): an issue reaching a terminal state
// drops its managed PRs with their ledger entries.
func TestPruneManagedPRsOnTerminal(t *testing.T) {
	o, state, issue := prEventsHarness(t, PREventAutomation{
		ID: "fix-ci", TriggerType: config.AutomationTriggerPRCheckCompleted, ProfileName: "fixer",
	})
	managePR(&state, issue, time.Now().Add(-time.Hour), "aaa")
	deliverPREvent(t, o, &state, issue, checkEvent("aaa", "ci", "failure", time.Now()))
	require.NotEmpty(t, state.PREventDispatched)
	pruneTerminalRuntimeLedgers(&state, map[string]struct{}{issue.Identifier: {}})
	assert.Empty(t, state.ManagedPRs)
	assert.Empty(t, state.PREventDispatched)
	assert.Empty(t, state.PREventBaselines)
}
