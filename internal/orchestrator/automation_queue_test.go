package orchestrator

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

func TestAutomationQueueKeyCronCoalescesSameAutomationAndIssue(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "issue-uuid", Identifier: "ENG-1"}
	first := AutomationDispatch{
		AutomationID: "nightly-sweep",
		Trigger: AutomationTriggerContext{
			Type:    config.AutomationTriggerCron,
			Cron:    "*/5 * * * *",
			FiredAt: time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
		},
	}
	second := first
	second.Trigger.FiredAt = time.Date(2026, 5, 20, 10, 5, 0, 0, time.UTC)

	require.Equal(t, automationQueueKey(issue, first), automationQueueKey(issue, second))
	require.Equal(t, "automation:nightly-sweep:cron:issue-uuid", automationQueueKey(issue, first))

	otherAutomation := first
	otherAutomation.AutomationID = "hourly-sweep"
	require.NotEqual(t, automationQueueKey(issue, first), automationQueueKey(issue, otherAutomation))

	otherIssue := domain.Issue{ID: "other-uuid", Identifier: "ENG-2"}
	require.NotEqual(t, automationQueueKey(issue, first), automationQueueKey(otherIssue, first))
}

func TestAutomationQueueKeyPROpenedIncludesPRURL(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "issue-uuid", Identifier: "ENG-1"}
	first := AutomationDispatch{
		AutomationID: "review-pr",
		Trigger: AutomationTriggerContext{
			Type:  config.AutomationTriggerPROpened,
			PRURL: "https://github.com/example/repo/pull/10",
		},
	}
	second := first
	second.Trigger.PRURL = "https://github.com/example/repo/pull/11"

	require.Equal(
		t,
		"automation:review-pr:pr_opened:issue-uuid:https://github.com/example/repo/pull/10",
		automationQueueKey(issue, first),
	)
	require.NotEqual(t, automationQueueKey(issue, first), automationQueueKey(issue, second))
}

func TestAutomationQueueKeyRateLimitedIncludesFailedProfileAndBackend(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "issue-uuid", Identifier: "ENG-1"}
	first := AutomationDispatch{
		AutomationID: "rate-limit-fallback",
		Trigger: AutomationTriggerContext{
			Type:          config.AutomationTriggerRateLimited,
			FailedProfile: "claude-coder",
			FailedBackend: "claude",
		},
	}
	second := first
	second.Trigger.FailedProfile = "codex-coder"
	require.Equal(
		t,
		"automation:rate-limit-fallback:rate_limited:issue-uuid:claude-coder:claude",
		automationQueueKey(issue, first),
	)
	require.NotEqual(t, automationQueueKey(issue, first), automationQueueKey(issue, second))

	third := first
	third.Trigger.FailedBackend = "codex"
	require.NotEqual(t, automationQueueKey(issue, first), automationQueueKey(issue, third))
}

func TestAutomationQueueKeyInputRequiredIncludesQuestionCommentKey(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "issue-uuid", Identifier: "ENG-1"}
	first := AutomationDispatch{
		AutomationID: "input-responder",
		Trigger: AutomationTriggerContext{
			Type:         config.AutomationTriggerInputRequired,
			InputContext: "Which file should I update?",
			CommentID:    "comment-1",
		},
	}
	second := first
	second.Trigger.CommentID = "comment-2"
	require.Equal(
		t,
		"automation:input-responder:input_required:issue-uuid:comment-1",
		automationQueueKey(issue, first),
	)
	require.NotEqual(t, automationQueueKey(issue, first), automationQueueKey(issue, second))

	withoutComment := first
	withoutComment.Trigger.CommentID = ""
	withoutComment.Trigger.InputContext = "Which test should I run?"
	otherWithoutComment := withoutComment
	otherWithoutComment.Trigger.InputContext = "Which file should I update?"
	require.NotEqual(
		t,
		automationQueueKey(issue, withoutComment),
		automationQueueKey(issue, otherWithoutComment),
	)
}

func TestAutomationQueueKeyRunFailedIncludesAttempt(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "issue-uuid", Identifier: "ENG-1"}
	first := AutomationDispatch{
		AutomationID: "failure-summarizer",
		Trigger: AutomationTriggerContext{
			Type:         config.AutomationTriggerRunFailed,
			RetryAttempt: 2,
		},
	}
	second := first
	second.Trigger.RetryAttempt = 3

	require.Equal(
		t,
		"automation:failure-summarizer:run_failed:issue-uuid:2",
		automationQueueKey(issue, first),
	)
	require.NotEqual(t, automationQueueKey(issue, first), automationQueueKey(issue, second))
}

func TestAutomationQueueKeyBlockersResolvedIncludesDependencyAuditVersion(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "issue-uuid", Identifier: "ENG-1"}
	first := AutomationDispatch{
		AutomationID: "unblock-backlog-to-todo",
		Trigger: AutomationTriggerContext{
			Type:                   config.AutomationTriggerBlockersResolved,
			DependencyAuditVersion: 7,
		},
	}
	second := first
	second.Trigger.DependencyAuditVersion = 8

	require.Equal(
		t,
		"automation:unblock-backlog-to-todo:blockers_resolved:issue-uuid:7",
		automationQueueKey(issue, first),
	)
	require.NotEqual(t, automationQueueKey(issue, first), automationQueueKey(issue, second))
}

func TestAutomationQueueKeyTestFireIncludesTimestamp(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{Identifier: "ENG-1"}
	first := AutomationDispatch{
		AutomationID: "manual-check",
		Trigger: AutomationTriggerContext{
			Type:    TestAutomationTriggerType,
			FiredAt: time.Date(2026, 5, 20, 10, 0, 0, 123, time.UTC),
		},
	}
	second := first
	second.Trigger.FiredAt = time.Date(2026, 5, 20, 10, 0, 1, 123, time.UTC)

	require.Equal(
		t,
		"automation:manual-check:test:ENG-1:"+strconv.FormatInt(first.Trigger.FiredAt.UnixNano(), 10),
		automationQueueKey(issue, first),
	)
	require.NotEqual(t, automationQueueKey(issue, first), automationQueueKey(issue, second))
}

func TestAutomationQueueEnqueueCoalescesExistingEntry(t *testing.T) {
	t.Parallel()

	state := NewState(&config.Config{})
	issue := domain.Issue{ID: "issue-uuid", Identifier: "ENG-1", Title: "Queued", State: "Todo"}
	firstAt := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(time.Minute)
	dispatch := AutomationDispatch{
		AutomationID: "nightly-sweep",
		ProfileName:  "codex",
		Instructions: "Inspect stale backlog work",
		Trigger: AutomationTriggerContext{
			Type:    config.AutomationTriggerCron,
			Cron:    "*/1 * * * *",
			FiredAt: firstAt,
		},
	}

	enqueueAutomation(&state, issue, dispatch, "no_slots", firstAt)
	dispatch.Trigger.FiredAt = secondAt
	enqueueAutomation(&state, issue, dispatch, "blocked_by:ENG-0", secondAt)

	require.Len(t, state.AutomationQueue, 1)
	require.Len(t, state.AutomationQueueOrder, 1)
	entry := state.AutomationQueue[state.AutomationQueueOrder[0]]
	require.Equal(t, "nightly-sweep", entry.AutomationID)
	require.Equal(t, config.AutomationTriggerCron, entry.TriggerType)
	require.Equal(t, issue.Identifier, entry.Issue.Identifier)
	require.Equal(t, AutomationQueueBlocked, entry.Status)
	require.Equal(t, AutomationQueueReasonBlockedBy, entry.Reason)
	require.Equal(t, "ENG-0", entry.ReasonDetail)
	require.Equal(t, firstAt, entry.QueuedAt)
	require.Equal(t, firstAt, entry.FiredAt)
	require.Equal(t, secondAt, entry.LastFiredAt)
	require.Equal(t, secondAt, entry.LastAttemptAt)
	require.Equal(t, 2, entry.AttemptCount)
}

func TestAutomationQueueSortedAndRemovePreserveFIFO(t *testing.T) {
	t.Parallel()

	state := NewState(&config.Config{})
	firstIssue := domain.Issue{ID: "issue-1", Identifier: "ENG-1", Title: "First", State: "Todo"}
	secondIssue := domain.Issue{ID: "issue-2", Identifier: "ENG-2", Title: "Second", State: "Todo"}
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	dispatch := AutomationDispatch{
		AutomationID: "nightly-sweep",
		Trigger:      AutomationTriggerContext{Type: config.AutomationTriggerCron},
	}

	enqueueAutomation(&state, firstIssue, dispatch, "no_slots", now)
	enqueueAutomation(&state, secondIssue, dispatch, "no_slots", now.Add(time.Second))
	firstID := automationQueueKey(firstIssue, dispatch)

	entries := sortedAutomationQueue(state)
	require.Len(t, entries, 2)
	require.Equal(t, "ENG-1", entries[0].Issue.Identifier)
	require.Equal(t, "ENG-2", entries[1].Issue.Identifier)

	removeAutomationQueueEntry(&state, firstID)
	require.NotContains(t, state.AutomationQueue, firstID)
	require.Equal(t, []string{automationQueueKey(secondIssue, dispatch)}, state.AutomationQueueOrder)
	require.Len(t, sortedAutomationQueue(state), 1)
}

func TestNewStateInitializesAutomationQueueAndDependencyAudit(t *testing.T) {
	t.Parallel()

	state := NewState(&config.Config{})

	require.NotNil(t, state.AutomationQueue)
	require.Empty(t, state.AutomationQueueOrder)
	require.NotNil(t, state.DependencyAudit)
	require.Zero(t, state.DependencyTransitionSeq)
}

func TestSnapshotDeepCopiesAutomationQueueAndDependencyAudit(t *testing.T) {
	t.Parallel()

	o := New(&config.Config{}, nil, nil, nil)
	state := NewState(&config.Config{})
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	label := "backend"
	blockerID := "blocker-id"
	blockerIdentifier := "ENG-0"
	blockerState := "Done"
	state.AutomationQueue["queue-1"] = &AutomationQueueEntry{
		ID:           "queue-1",
		AutomationID: "nightly-sweep",
		Issue: domain.Issue{
			ID:         "issue-uuid",
			Identifier: "ENG-1",
			Labels:     []string{label},
			BlockedBy: []domain.BlockerRef{{
				ID:         &blockerID,
				Identifier: &blockerIdentifier,
				State:      &blockerState,
			}},
		},
		Reason:   AutomationQueueReasonNoSlots,
		QueuedAt: now,
	}
	state.AutomationQueueOrder = []string{"queue-1"}
	state.DependencyAudit["ENG-1"] = &DependencyAuditEntry{
		Identifier:           "ENG-1",
		Sources:              []DependencyAuditSource{DependencySourceTrackerRelation},
		BlockedBy:            []domain.BlockerRef{{Identifier: &blockerIdentifier}},
		ResolvedBlockers:     []domain.BlockerRef{{Identifier: &blockerIdentifier}},
		LastAuditedAt:        now,
		LastTransitionReason: "all_blockers_terminal",
	}

	o.storeSnap(state)
	state.AutomationQueue["queue-1"].Issue.Identifier = "MUTATED"
	state.AutomationQueue["queue-1"].Issue.Labels[0] = "mutated"
	state.AutomationQueueOrder[0] = "mutated-order"
	state.DependencyAudit["ENG-1"].Identifier = "MUTATED"
	state.DependencyAudit["ENG-1"].Sources[0] = DependencySourceIssueText

	snap := o.Snapshot()
	require.Equal(t, []string{"queue-1"}, snap.AutomationQueueOrder)
	require.Equal(t, "ENG-1", snap.AutomationQueue["queue-1"].Issue.Identifier)
	require.Equal(t, []string{label}, snap.AutomationQueue["queue-1"].Issue.Labels)
	require.Equal(t, "ENG-1", snap.DependencyAudit["ENG-1"].Identifier)
	require.Equal(t, []DependencyAuditSource{DependencySourceTrackerRelation}, snap.DependencyAudit["ENG-1"].Sources)

	snap.AutomationQueue["queue-1"].Issue.Labels[0] = "snapshot-mutated"
	snap.DependencyAudit["ENG-1"].Sources[0] = DependencySourceIssueComment
	secondSnap := o.Snapshot()
	require.Equal(t, []string{label}, secondSnap.AutomationQueue["queue-1"].Issue.Labels)
	require.Equal(t, []DependencyAuditSource{DependencySourceTrackerRelation}, secondSnap.DependencyAudit["ENG-1"].Sources)
}

func TestAutomationQueueBackpressureRejectsNewEntryAtCapButAllowsCoalesce(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Agent.MaxAutomationQueueLength = 1
	state := NewState(cfg)
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	firstIssue := domain.Issue{ID: "issue-1", Identifier: "ENG-1", Title: "First", State: "Todo"}
	secondIssue := domain.Issue{ID: "issue-2", Identifier: "ENG-2", Title: "Second", State: "Todo"}
	dispatch := AutomationDispatch{
		AutomationID: "nightly-cron",
		ProfileName:  "default",
		Trigger:      AutomationTriggerContext{Type: config.AutomationTriggerCron},
	}

	require.True(t, enqueueAutomation(&state, firstIssue, dispatch, "no_slots", now))
	require.True(t, state.AutomationQueueBackpressure.Saturated)
	require.True(t, state.AutomationQueueBackpressure.PausedProducers)
	require.Equal(t, 1, state.AutomationQueueBackpressure.Length)
	require.Equal(t, 1, state.AutomationQueueBackpressure.MaxLength)

	require.True(t, enqueueAutomation(&state, firstIssue, dispatch, "no_slots", now.Add(time.Minute)))
	require.Len(t, state.AutomationQueue, 1)
	require.Equal(t, 2, state.AutomationQueue[state.AutomationQueueOrder[0]].AttemptCount)
	require.Zero(t, state.AutomationQueueBackpressure.RejectedSinceBoot)

	require.False(t, enqueueAutomation(&state, secondIssue, dispatch, "no_slots", now.Add(2*time.Minute)))
	require.Len(t, state.AutomationQueue, 1)
	require.Equal(t, 1, state.AutomationQueueBackpressure.RejectedSinceBoot)
	require.False(t, state.AutomationQueueBackpressure.LastRejectedAt.IsZero())
	require.Contains(t, state.AutomationQueueBackpressure.LastRejectedReason, "queue_full")
	require.Contains(t, state.AutomationQueueBackpressure.LastRejectedReason, "ENG-2")
}

func TestAutomationQueueBackpressureResumesBelowLowWater(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Agent.MaxAutomationQueueLength = 5
	state := NewState(cfg)
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	dispatch := AutomationDispatch{
		AutomationID: "nightly-cron",
		ProfileName:  "default",
		Trigger:      AutomationTriggerContext{Type: config.AutomationTriggerCron},
	}

	for i := 1; i <= 5; i++ {
		issue := domain.Issue{
			ID:         "issue-" + strconv.Itoa(i),
			Identifier: "ENG-" + strconv.Itoa(i),
			Title:      "Queued",
			State:      "Todo",
		}
		require.True(t, enqueueAutomation(&state, issue, dispatch, "no_slots", now))
	}
	require.True(t, state.AutomationQueueBackpressure.PausedProducers)

	removeAutomationQueueEntry(&state, automationQueueKey(domain.Issue{ID: "issue-1", Identifier: "ENG-1"}, dispatch))
	require.True(t, state.AutomationQueueBackpressure.PausedProducers, "len 4 is the 80% low-water mark, not below it")
	removeAutomationQueueEntry(&state, automationQueueKey(domain.Issue{ID: "issue-2", Identifier: "ENG-2"}, dispatch))

	require.Equal(t, 3, state.AutomationQueueBackpressure.Length)
	require.False(t, state.AutomationQueueBackpressure.Saturated)
	require.False(t, state.AutomationQueueBackpressure.PausedProducers)
}
