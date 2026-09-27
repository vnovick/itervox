package server

import "context"

// Merge runs the gh-CLI gates and the actual merge. Returns (commit, "", nil)
// on success; (_, reason, nil) on a precondition refusal; (_, _, err) only on
// gh CLI invocation failures the operator should look at directly. It is
// MergeResult without the PR metadata.
func (g MergePRGate) Merge(ctx context.Context, pr int) (string, string, error) {
	res, err := g.MergeResult(ctx, pr)
	return res.Commit, res.Reason, err
}
