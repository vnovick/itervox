package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

func verdicts(vals ...string) []ReviewVerdict {
	out := make([]ReviewVerdict, 0, len(vals))
	for i, v := range vals {
		out = append(out, ReviewVerdict{Profile: string(rune('a' + i)), Verdict: v})
	}
	return out
}

// TestNormalizeReviewVerdictFailsClosed is the security-relevant case: only an
// explicit "approve" approves. Anything else — empty, garbage, a truncated
// write from a crashed reviewer — must block, so a reviewer bug can never
// become a silent auto-approve.
func TestNormalizeReviewVerdictFailsClosed(t *testing.T) {
	require.Equal(t, ReviewVerdictApprove, NormalizeReviewVerdict("approve"))
	require.Equal(t, ReviewVerdictApprove, NormalizeReviewVerdict("  APPROVE  "), "case and padding tolerated")

	for _, raw := range []string{"", "block", "nope", "approved", "yes", "{", "APPROVE!"} {
		require.Equal(t, ReviewVerdictBlock, NormalizeReviewVerdict(raw),
			"unrecognized verdict %q must fail closed to block", raw)
	}
}

func TestEvaluateReviewQuorumAnyBlock(t *testing.T) {
	out := EvaluateReviewQuorum(verdicts("approve", "approve", "block"), config.ReviewQuorumAnyBlock)
	require.True(t, out.Blocked, "a single block must block under any_block")
	require.Equal(t, 2, out.Approvals)
	require.Equal(t, 1, out.Blocks)

	clean := EvaluateReviewQuorum(verdicts("approve", "approve"), config.ReviewQuorumAnyBlock)
	require.False(t, clean.Blocked)
}

func TestEvaluateReviewQuorumMajority(t *testing.T) {
	// 1 of 3 is not a majority.
	require.False(t, EvaluateReviewQuorum(verdicts("block", "approve", "approve"), config.ReviewQuorumMajority).Blocked)
	// 2 of 3 is.
	require.True(t, EvaluateReviewQuorum(verdicts("block", "block", "approve"), config.ReviewQuorumMajority).Blocked)
	// Even split of 2 is NOT a majority — strictly more than half is required.
	require.False(t, EvaluateReviewQuorum(verdicts("block", "approve"), config.ReviewQuorumMajority).Blocked)
}

func TestEvaluateReviewQuorumUnanimous(t *testing.T) {
	require.True(t, EvaluateReviewQuorum(verdicts("block", "block"), config.ReviewQuorumUnanimous).Blocked)
	require.False(t, EvaluateReviewQuorum(verdicts("block", "approve"), config.ReviewQuorumUnanimous).Blocked,
		"one approval defeats a unanimous block")
}

// TestEvaluateReviewQuorumUnknownRuleIsStrictest guards the fail-closed
// default for a State assembled outside the config loader.
func TestEvaluateReviewQuorumUnknownRuleIsStrictest(t *testing.T) {
	out := EvaluateReviewQuorum(verdicts("approve", "block"), "not-a-real-rule")
	require.True(t, out.Blocked, "an unrecognized quorum rule must fall back to the strictest behavior")
}

// TestEvaluateReviewQuorumEmptyIsNotBlocked: no reviewer ran, so there is
// nothing to gate on. Blocking here would deadlock any issue whose reviewers
// never dispatched.
func TestEvaluateReviewQuorumEmptyIsNotBlocked(t *testing.T) {
	out := EvaluateReviewQuorum(nil, config.ReviewQuorumAnyBlock)
	require.False(t, out.Blocked)
	require.Equal(t, 0, out.Approvals)
	require.Equal(t, 0, out.Blocks)
}

func TestReviewerProfileChainFallsBackToSingleProfile(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.ReviewerProfile = "reviewer"
	require.Equal(t, []string{"reviewer"}, ReviewerProfileChain(cfg),
		"an existing single-reviewer config must keep working unchanged")

	cfg.Agent.ReviewerProfiles = []string{"security", "correctness"}
	require.Equal(t, []string{"security"}, ReviewerProfileChain(cfg),
		"the list form wins when set, but fan-out is gated for this release: only the first entry runs")
}

func TestReviewerProfileChainDropsBlanksAndHandlesNil(t *testing.T) {
	require.Nil(t, ReviewerProfileChain(nil))
	require.Empty(t, ReviewerProfileChain(&config.Config{}))

	cfg := &config.Config{}
	cfg.Agent.ReviewerProfiles = []string{"  ", "", "a", "b"}
	require.Equal(t, []string{"a"}, ReviewerProfileChain(cfg),
		"blanks are dropped BEFORE the fan-out truncation, so a leading blank never becomes the reviewer")
}

func TestUpsertReviewVerdictReplacesSameProfile(t *testing.T) {
	list := []ReviewVerdict{{Profile: "security", Verdict: ReviewVerdictBlock}}
	list = UpsertReviewVerdict(list, ReviewVerdict{Profile: "correctness", Verdict: ReviewVerdictApprove})
	require.Len(t, list, 2)

	list = UpsertReviewVerdict(list, ReviewVerdict{Profile: "security", Verdict: ReviewVerdictApprove})
	require.Len(t, list, 2, "a re-run must replace its own verdict, not add a second vote")
	require.Equal(t, ReviewVerdictApprove, list[0].Verdict)
}

func TestReadReviewVerdictRoundTrip(t *testing.T) {
	workdir := t.TempDir()
	dir := ReviewDir(workdir, "ENG-1", "security")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body, err := json.Marshal(ReviewVerdict{Verdict: "approve", Reasons: []string{"looks fine"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ReviewVerdictFileName), body, 0o600))

	now := time.Now()
	got, err := ReadReviewVerdict(workdir, "ENG-1", "security", now)
	require.NoError(t, err)
	require.Equal(t, ReviewVerdictApprove, got.Verdict)
	require.Equal(t, "security", got.Profile, "profile is stamped from the path, not trusted from the file")
	require.Equal(t, []string{"looks fine"}, got.Reasons)
	require.Equal(t, now, got.RecordedAt)
}

// TestReadReviewVerdictMalformedIsAnError: the caller converts a read error
// into a blocking verdict, so this must surface rather than silently
// returning an empty (and therefore block-normalized but reason-less) value.
func TestReadReviewVerdictMalformedIsAnError(t *testing.T) {
	workdir := t.TempDir()
	dir := ReviewDir(workdir, "ENG-1", "security")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ReviewVerdictFileName), []byte("{not json"), 0o600))

	_, err := ReadReviewVerdict(workdir, "ENG-1", "security", time.Now())
	require.Error(t, err)
}

func TestReadReviewVerdictMissingFileIsAnError(t *testing.T) {
	_, err := ReadReviewVerdict(t.TempDir(), "ENG-1", "security", time.Now())
	require.Error(t, err, "a reviewer that recorded no verdict must not read as an approval")
}
