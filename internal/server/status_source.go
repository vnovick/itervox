package server

import "context"

type issueStatusSourceKey struct{}

const (
	IssueStatusSourceDashboard = "dashboard"
	IssueStatusSourceAgent     = "agent_action"
)

func WithIssueStatusSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, issueStatusSourceKey{}, source)
}

func IssueStatusSource(ctx context.Context) string {
	value, _ := ctx.Value(issueStatusSourceKey{}).(string)
	return value
}
