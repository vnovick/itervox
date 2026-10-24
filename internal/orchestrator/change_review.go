package orchestrator

// change_review.go — #238: read-only lookups the change-review endpoints
// need, from the published snapshot. Safe from any goroutine (an HTTP
// handler); they never send an event or touch the loop's State.

// IssuePRURL returns the pull request a run of identifier recorded most
// recently (a managed PR, #173); "" when the issue has none.
func (o *Orchestrator) IssuePRURL(identifier string) string {
	if identifier == "" {
		return ""
	}
	o.snapMu.RLock()
	defer o.snapMu.RUnlock()
	best := ManagedPR{}
	for _, pr := range o.lastSnap.ManagedPRs {
		if pr.Identifier == identifier && (best.PRURL == "" || pr.RecordedAt.After(best.RecordedAt)) {
			best = pr
		}
	}
	return best.PRURL
}

// WorktreePath returns identifier's workspace directory on this host, ""
// when it has none.
func (o *Orchestrator) WorktreePath(identifier string) string {
	p, err := o.worktreePath(identifier)
	if err != nil {
		return ""
	}
	return p
}
