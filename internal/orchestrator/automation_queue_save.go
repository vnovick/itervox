package orchestrator

import (
	"maps"
	"reflect"
	"slices"
)

// automationQueueSave is the automation-queue state last persisted, kept so
// storeSnap can skip re-encoding a queue that has not changed (#210).
type automationQueueSave struct {
	path string
	disk automationQueueStateDisk
}

// matches reports whether the given queue state equals the saved copy in
// every persisted field. It compares values, not identities, so an in-place
// edit of an entry or audit row counts as a change, and it errs towards
// "changed": a nil-versus-empty map only costs one redundant encode.
func (d automationQueueStateDisk) matches(entries map[string]*AutomationQueueEntry, order []string, backpressure AutomationQueueBackpressure, dependencyAudit map[string]*DependencyAuditEntry, dependencyTransitionSeq int64, cronCursors map[string]string) bool {
	return d.DependencyTransitionSeq == dependencyTransitionSeq &&
		slices.Equal(d.Order, order) &&
		maps.Equal(d.CronCursors, cronCursors) &&
		reflect.DeepEqual(d.Backpressure, backpressure) &&
		reflect.DeepEqual(d.Entries, entries) &&
		reflect.DeepEqual(d.DependencyAudit, dependencyAudit)
}
