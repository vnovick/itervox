package guardrails

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/gitexec"
)

// maxAuditPatchBytes bounds the -U0 patch AuditRange reads: a larger diff
// is audited up to here (Truncated), never read whole into memory. The
// file list and the untracked files of a working tree are read within the
// same bound.
var maxAuditPatchBytes = 4 << 20 // a var so tests can lower it

// auditRangeTimeout bounds one AuditRange's git calls from the hook.
const auditRangeTimeout = 60 * time.Second

// Untracked files AuditRange reads for the working tree: at most this
// many, each at most maxAuditUntrackedFileBytes (a larger one is skipped
// and the result marked Truncated), all within what is left of
// maxAuditPatchBytes after the patch.
const (
	maxAuditUntrackedFiles     = 2000
	maxAuditUntrackedFileBytes = 1 << 20
	auditBinarySniffBytes      = 8000
)

// AuditResult is AuditRange's answer.
type AuditResult struct {
	Findings []AuditFinding
	// Paths are the changed paths of the range in git's order (for the
	// working tree, the untracked files after the tracked ones), as many
	// as the read caps allow.
	Paths []string
	// Truncated: the file list or the patch was larger than its read cap,
	// or an untracked file was not read, so the range was audited only in
	// part.
	Truncated bool
	// PathsTruncated: Paths is not every changed path (a file list was
	// larger than its read cap). Implies Truncated.
	PathsTruncated bool
}

// AuditRange applies the audit rules (the referee's table) to the diff in
// the git repository at dir from commit from to commit to — to "" is the
// working tree (the Lanes view's live worktree), its untracked files
// included as all-added files (read through an os.Root of dir, regular
// files only; the index is never touched). Runs git through gitexec on the
// caller's goroutine; ctx bounds it.
func AuditRange(ctx context.Context, dir, from, to string) (AuditResult, error) {
	rangeArgs := []string{from}
	if to != "" {
		rangeArgs = append(rangeArgs, to)
	}
	status, statusTruncated, err := boundedGit(ctx, dir, maxAuditPatchBytes,
		append(append([]string{"diff", "--name-status", "-z", "--no-renames", "--no-ext-diff"}, rangeArgs...), "--")...)
	if err != nil {
		return AuditResult{}, fmt.Errorf("guardrails: audit: name-status: %w", err)
	}
	if statusTruncated { // whole records only: cut after the last NUL
		status = status[:bytes.LastIndexByte(status, 0)+1]
	}
	res := AuditResult{Findings: AuditDeletedTests(status), Paths: nameStatusPaths(status),
		Truncated: statusTruncated, PathsTruncated: statusTruncated}
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
	res.Truncated = res.Truncated || truncated
	if to == "" {
		if err := auditUntracked(ctx, dir, maxAuditPatchBytes-len(patch), &res); err != nil {
			return AuditResult{}, fmt.Errorf("guardrails: audit: untracked: %w", err)
		}
	}
	return res, nil
}

// nameStatusPaths are the paths of `git diff --name-status -z
// --no-renames` output (status, path pairs).
func nameStatusPaths(status []byte) []string {
	fields := bytes.Split(status, []byte{0})
	out := make([]string, 0, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		if len(fields[i+1]) > 0 {
			out = append(out, string(fields[i+1]))
		}
	}
	return out
}

// auditUntracked adds the working tree's untracked files (not ignored) to
// res: every path, and the line rules' findings on each line of the first
// maxAuditUntrackedFiles text files, read within budget bytes. Every line
// of an untracked file is new, so each is audited as an added line
// numbered from 1. A path that is not a regular file (a symlink, a FIFO) is
// listed but never opened, and root keeps every read inside dir.
func auditUntracked(ctx context.Context, dir string, budget int, res *AuditResult) error {
	list, truncated, err := boundedGit(ctx, dir, maxAuditPatchBytes, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	if truncated {
		list = list[:bytes.LastIndexByte(list, 0)+1]
		res.Truncated, res.PathsTruncated = true, true
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	read := 0
	for p := range strings.SplitSeq(string(list), "\x00") {
		if p == "" {
			continue
		}
		res.Paths = append(res.Paths, p)
		if err := ctx.Err(); err != nil {
			return err
		}
		if read == maxAuditUntrackedFiles {
			res.Truncated = true
			continue
		}
		data, ok := readUntracked(root, p, budget)
		if !ok {
			res.Truncated = true
			continue
		}
		if data == nil {
			continue
		}
		read++
		budget -= len(data)
		res.Findings = append(res.Findings, auditAddedFile(p, data)...)
	}
	return nil
}

// readUntracked reads one untracked file for the audit: nil, true for a
// file with nothing to audit (not regular, empty, unreadable, binary);
// false when it is larger than the per-file cap or what is left of budget.
func readUntracked(root *os.Root, p string, budget int) ([]byte, bool) {
	info, err := root.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, true
	}
	if info.Size() > maxAuditUntrackedFileBytes || info.Size() > int64(budget) {
		return nil, false
	}
	f, err := root.Open(p)
	if err != nil {
		return nil, true
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, info.Size()))
	if err != nil || bytes.IndexByte(data[:min(len(data), auditBinarySniffBytes)], 0) >= 0 {
		return nil, true
	}
	return data, true
}

// auditAddedFile applies the line rules to every line of a new file.
func auditAddedFile(p string, data []byte) []AuditFinding {
	var out []AuditFinding
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), maxAuditUntrackedFileBytes+1)
	for n := 1; sc.Scan(); n++ {
		out = appendLineFindings(out, p, n, sc.Text())
	}
	return out
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
