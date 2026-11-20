package guardrails

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/vnovick/itervox/internal/gitexec"
)

// maxAuditPatchBytes bounds the -U0 patch AuditRange reads: a larger diff
// is audited up to here (Truncated), never read whole into memory.
const maxAuditPatchBytes = 4 << 20

// auditRangeTimeout bounds one AuditRange's git calls from the hook.
const auditRangeTimeout = 60 * time.Second

// AuditResult is AuditRange's answer.
type AuditResult struct {
	Findings []AuditFinding
	// Truncated: the patch was larger than the read cap and was audited
	// only up to it.
	Truncated bool
}

// AuditRange applies the audit rules (the referee's table) to the diff in
// the git repository at dir from commit from to commit to — to "" is the
// working tree (the Lanes view's live worktree). Runs git through gitexec
// on the caller's goroutine; ctx bounds it.
func AuditRange(ctx context.Context, dir, from, to string) (AuditResult, error) {
	rangeArgs := []string{from}
	if to != "" {
		rangeArgs = append(rangeArgs, to)
	}
	status, _, err := boundedGit(ctx, dir, maxAuditPatchBytes,
		append(append([]string{"diff", "--name-status", "-z", "--no-renames", "--no-ext-diff"}, rangeArgs...), "--")...)
	if err != nil {
		return AuditResult{}, fmt.Errorf("guardrails: audit: name-status: %w", err)
	}
	res := AuditResult{Findings: AuditDeletedTests(status)}
	patch, truncated, err := boundedGit(ctx, dir, maxAuditPatchBytes,
		append(append([]string{"-c", "core.quotePath=false", "diff", "-U0", "--no-renames", "--no-color", "--no-ext-diff", "--no-textconv",
			"--src-prefix=a/", "--dst-prefix=b/"}, rangeArgs...), "--")...)
	if err != nil {
		return AuditResult{}, fmt.Errorf("guardrails: audit: diff: %w", err)
	}
	if truncated { // audit whole lines only
		if i := bytes.LastIndexByte(patch, '\n'); i >= 0 {
			patch = patch[:i+1]
		}
	}
	found, err := AuditPatch(patch)
	if err != nil {
		return AuditResult{}, fmt.Errorf("guardrails: audit: %w", err)
	}
	res.Findings = append(res.Findings, found...)
	res.Truncated = truncated
	return res, nil
}

// boundedGit runs git and reads at most limit bytes of its output; a
// longer output stops git and reports truncated.
func boundedGit(ctx context.Context, dir string, limit int, args ...string) (out []byte, truncated bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := gitexec.Command(ctx, dir, args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	out, readErr := io.ReadAll(io.LimitReader(pipe, int64(limit)+1))
	if len(out) > limit {
		out, truncated = out[:limit], true
		cancel() // stop git: the rest is not read
	}
	waitErr := cmd.Wait()
	switch {
	case readErr != nil && !truncated:
		return nil, false, readErr
	case waitErr != nil && !truncated:
		return nil, false, waitErr
	}
	return out, truncated, nil
}

// AuditRange is the hook's AuditRange: from..to with the hook's own
// timeout.
func (g gitRepo) AuditRange(from, to string) ([]AuditFinding, error) {
	ctx, cancel := context.WithTimeout(context.Background(), auditRangeTimeout)
	defer cancel()
	res, err := AuditRange(ctx, g.dir, from, to)
	return res.Findings, err
}
