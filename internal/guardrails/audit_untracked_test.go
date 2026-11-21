package guardrails

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The working tree's audit includes untracked files as all-added files
// (line numbers from 1), lists them in Paths, never follows a symlink or
// reads a binary, and never touches the index.
func TestAuditRangeWorktreeIncludesUntrackedFiles(t *testing.T) {
	r := newRig(t, nil)
	head := strings.TrimSpace(git(t, r.work, "rev-parse", "HEAD"))
	write := func(rel, body string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(r.work, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(r.work, rel), []byte(body), 0o644))
	}
	write(".github/workflows/new.yml", "jobs:\n  t:\n    steps:\n      - run: make test || true\n")
	write("bin.yml", "\x00continue-on-error: true\n") // binary: not read
	outside := filepath.Join(r.root, "outside.yml")
	require.NoError(t, os.WriteFile(outside, []byte("continue-on-error: true\n"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(r.work, ".github", "workflows", "link.yml")))

	res, err := AuditRange(context.Background(), r.work, head, "")
	require.NoError(t, err)
	var got []string
	for _, f := range res.Findings {
		got = append(got, FindingReason(f).Location()+" "+f.Rule)
	}
	assert.Equal(t, []string{".github/workflows/new.yml:4 ci_or_true"}, got, "the untracked workflow is audited; the symlink and the binary are not read")
	assert.ElementsMatch(t, []string{".github/workflows/link.yml", ".github/workflows/new.yml", "bin.yml"}, res.Paths)
	assert.False(t, res.Truncated)
	assert.Empty(t, git(t, r.work, "diff", "--cached", "--name-only"), "the index is never touched")
	assert.Contains(t, git(t, r.work, "status", "--porcelain"), "?? .github/", "the files stay untracked")

	// A commit range never reads the working tree.
	res, err = AuditRange(context.Background(), r.work, head, head)
	require.NoError(t, err)
	assert.Empty(t, res.Findings)
	assert.Empty(t, res.Paths)
}

// A file list larger than the read cap is cut at a whole record and marks
// the result Truncated and PathsTruncated (it no longer passes for every
// changed path); so does an untracked file over what is left of the cap.
func TestAuditRangeTruncatedFileListIsMarked(t *testing.T) {
	r := newRig(t, nil)
	base := strings.TrimSpace(git(t, r.work, "rev-parse", "HEAD"))
	for i := range 40 {
		r.writeCommit(fmt.Sprintf("dir/file-with-a-long-name-%03d.txt", i), "x\n", "add")
	}
	head := strings.TrimSpace(git(t, r.work, "rev-parse", "HEAD"))
	full, err := AuditRange(context.Background(), r.work, base, head)
	require.NoError(t, err)
	require.Len(t, full.Paths, 40)
	assert.False(t, full.Truncated)

	old := maxAuditPatchBytes
	t.Cleanup(func() { maxAuditPatchBytes = old })
	maxAuditPatchBytes = 500
	cut, err := AuditRange(context.Background(), r.work, base, head)
	require.NoError(t, err)
	assert.True(t, cut.Truncated)
	assert.True(t, cut.PathsTruncated)
	require.NotEmpty(t, cut.Paths)
	assert.Less(t, len(cut.Paths), 40)
	for _, p := range cut.Paths {
		assert.Regexp(t, `^dir/file-with-a-long-name-\d{3}\.txt$`, p, "only whole records")
	}

	maxAuditPatchBytes = old
	require.NoError(t, os.WriteFile(filepath.Join(r.work, "big.txt"), []byte(strings.Repeat("y\n", maxAuditUntrackedFileBytes)), 0o644))
	wt, err := AuditRange(context.Background(), r.work, head, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"big.txt"}, wt.Paths)
	assert.True(t, wt.Truncated, "an untracked file over the cap is not audited, and that is said")
	assert.False(t, wt.PathsTruncated)
}

// linearBound has headroom for a loaded CI runner under -race (the
// 200k-finding tests take a few hundred milliseconds here); the quadratic
// code took minutes at that size.
const linearBound = 3 * time.Second

// De-duplication is linear: 200k findings (half of them repeats) over 20k
// files go through findingSet in well under a second, keep MaxLaneReasons
// and count every distinct one. (The quadratic scan it replaces took tens
// of seconds at 120k.)
func TestFindingSetIsLinear(t *testing.T) {
	const n = 200_000
	found := make([]AuditFinding, 0, n)
	paths := make([]string, 0, n/10)
	for i := range n / 2 {
		p := fmt.Sprintf("dir/f%05d.yml", i%(n/10))
		if i < n/10 {
			paths = append(paths, p)
		}
		f := AuditFinding{Rule: "ci_or_true", Path: p, Line: i, Message: "m"}
		found = append(found, f, f)
	}
	start := time.Now()
	set := newFindingSet(MaxLaneReasons)
	set.add(found, pathSet(paths))
	reasons, total := LaneReasons(LaneResult{Lane: LaneHuman, Human: paths, Rules: map[string]string{}}, set.kept, set.total)
	elapsed := time.Since(start)
	assert.Equal(t, n/2, set.total)
	assert.Len(t, set.kept, MaxLaneReasons)
	assert.Len(t, reasons, MaxLaneReasons)
	assert.Equal(t, n/2+len(paths), total)
	assert.Less(t, elapsed, linearBound, "de-duplicating %d findings took %s", n, elapsed)
}

// The audit diff carries AuditContext lines of context (so a hunk rule
// sees what a change sits inside), and the read cap applies to that larger
// patch: a range whose -U0 patch would fit is cut at the cap, audited up
// to the last whole line and marked Truncated. Line numbers stay the head
// version's.
func TestAuditRangeContextPatchIsCappedAndMarked(t *testing.T) {
	r := newRig(t, nil)
	const wf = ".github/workflows/ci.yml"
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = fmt.Sprintf("      - run: step %03d", i+1)
	}
	r.writeCommit(wf, strings.Join(lines, "\n")+"\n", "base")
	base := strings.TrimSpace(git(t, r.work, "rev-parse", "HEAD"))
	lines[99] = "      - run: make test || true"
	lines[179] = "      - run: make lint || true"
	r.writeCommit(wf, strings.Join(lines, "\n")+"\n", "weaken")
	head := strings.TrimSpace(git(t, r.work, "rev-parse", "HEAD"))

	locations := func(res AuditResult) []string {
		var out []string
		for _, f := range res.Findings {
			out = append(out, FindingReason(f).Location()+" "+f.Rule)
		}
		return out
	}
	full, err := AuditRange(context.Background(), r.work, base, head)
	require.NoError(t, err)
	assert.Equal(t, []string{wf + ":100 ci_or_true", wf + ":180 ci_or_true"}, locations(full))
	assert.False(t, full.Truncated)

	ctxPatch := git(t, r.work, "diff", fmt.Sprintf("-U%d", AuditContext), "--no-renames", "--no-color", base, head, "--")
	zeroPatch := git(t, r.work, "diff", "-U0", "--no-renames", "--no-color", base, head, "--")
	second := strings.Index(ctxPatch, "+      - run: make lint || true")
	require.Positive(t, second)
	require.Less(t, len(zeroPatch), second, "the -U0 patch would fit under the cap below")

	old := maxAuditPatchBytes
	t.Cleanup(func() { maxAuditPatchBytes = old })
	maxAuditPatchBytes = second + 5 // inside the second change's line
	cut, err := AuditRange(context.Background(), r.work, base, head)
	require.NoError(t, err)
	assert.True(t, cut.Truncated, "the context patch is larger than the cap, and that is said")
	assert.False(t, cut.PathsTruncated)
	assert.Equal(t, []string{wf + ":100 ci_or_true"}, locations(cut), "audited up to the last whole line")
}

// AuditRange's diff carries context, so the Lanes view and the pre-push
// lane reasons see a test file added to an existing multi-line Vitest
// exclude (its opener is a context line), as `referee audit` does.
func TestAuditRangeHunkRulesSeeContext(t *testing.T) {
	r := newRig(t, nil)
	cfg := "import { defineConfig } from 'vitest/config';\n\nexport default defineConfig({\n  test: {\n    exclude: [\n      'node_modules',\n%s    ],\n  },\n});\n"
	r.writeCommit("vitest.config.ts", fmt.Sprintf(cfg, ""), "base")
	base := strings.TrimSpace(git(t, r.work, "rev-parse", "HEAD"))
	r.writeCommit("vitest.config.ts", fmt.Sprintf(cfg, "      'src/flaky.test.ts',\n"), "exclude")
	head := strings.TrimSpace(git(t, r.work, "rev-parse", "HEAD"))
	for _, to := range []string{head, ""} { // the commit range and the worktree
		res, err := AuditRange(context.Background(), r.work, base, to)
		require.NoError(t, err)
		var got []string
		for _, f := range res.Findings {
			got = append(got, FindingReason(f).Location()+" "+f.Rule)
		}
		assert.Equal(t, []string{"vitest.config.ts:7 test_path_ignored"}, got, "to %q", to)
	}
}

// An untracked file goes through the hunk rules as one all-added change
// (as the patch reader reads a new tracked file): a test file added to a
// multi-line Vitest exclude in a new config is a finding.
func TestAuditRangeUntrackedFileRunsHunkRules(t *testing.T) {
	r := newRig(t, nil)
	head := strings.TrimSpace(git(t, r.work, "rev-parse", "HEAD"))
	body := "import { defineConfig } from 'vitest/config';\n\nexport default defineConfig({\n  test: {\n    exclude: [\n      'node_modules',\n      'src/flaky.test.ts',\n    ],\n  },\n});\n"
	require.NoError(t, os.WriteFile(filepath.Join(r.work, "vitest.config.ts"), []byte(body), 0o644))
	res, err := AuditRange(context.Background(), r.work, head, "")
	require.NoError(t, err)
	var got []string
	for _, f := range res.Findings {
		got = append(got, FindingReason(f).Location()+" "+f.Rule)
	}
	assert.Equal(t, []string{"vitest.config.ts:7 test_path_ignored"}, got)
}
