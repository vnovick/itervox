package server

import "time"

// IssueReviewRow is an issue's latest review in issue detail (#135).
type IssueReviewRow struct {
	// Verdicts holds one entry per reviewer that has finished, in the order
	// they finished.
	Verdicts []ReviewVerdictRow `json:"verdicts"`
	// Outcome is the quorum result; nil while reviewers are still running.
	Outcome *ReviewOutcomeRow `json:"outcome,omitempty"`
	// InProgress is set while a multi-reviewer chain is still running.
	InProgress bool `json:"inProgress,omitempty"`
}

// ReviewVerdictRow is one reviewer's verdict.
type ReviewVerdictRow struct {
	Profile    string             `json:"profile"`
	Verdict    string             `json:"verdict"` // "approve" or "block"
	Reasons    []string           `json:"reasons,omitempty"`
	Comments   []ReviewCommentRow `json:"comments,omitempty"`
	RecordedAt time.Time          `json:"recordedAt"`
}

// ReviewCommentRow is a reviewer's line comment.
type ReviewCommentRow struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Body string `json:"body"`
}

// ReviewOutcomeRow is the quorum result over every verdict.
type ReviewOutcomeRow struct {
	Blocked   bool   `json:"blocked"`
	Approvals int    `json:"approvals"`
	Blocks    int    `json:"blocks"`
	Quorum    string `json:"quorum,omitempty"`
}
