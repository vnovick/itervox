package memory

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func entry(id, text string, mods ...func(*Entry)) Entry {
	e := Entry{ID: id, Type: TypeFact, Text: text, Scope: ScopeProject, Status: StatusActive,
		Updated: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func pin(e *Entry) { e.Pinned = true }

// TestMemoryPinnedAlwaysIncludedRestWithinBudget (#202): pinned entries are
// always in the selection, even when they alone exceed the budget; the rest
// are ranked against the issue and profile and added in rank order while
// the rendered block fits the budget, so overflow drops the lowest-ranked
// entries first. Proposed, archived and out-of-profile entries are never
// used.
func TestMemoryPinnedAlwaysIncludedRestWithinBudget(t *testing.T) {
	filler := strings.Repeat("filler words here ", 12) // ~55 tokens each
	entries := []Entry{
		entry("pinned-a", "Keep PR descriptions under ten lines.", pin),
		entry("billing", "The billing webhook retries three times; "+filler),
		entry("pnpm", "This repo uses pnpm for the web package; "+filler, func(e *Entry) { e.Tags = []string{"web"} }),
		entry("unrelated", "Staging database is read-only on Fridays; "+filler),
		entry("proposed", "pnpm web billing", func(e *Entry) { e.Status = StatusProposed }),
		entry("archived", "pnpm web billing", func(e *Entry) { e.Status = StatusArchived }),
		entry("reviewer-only", "pnpm web billing", func(e *Entry) { e.Profiles = []string{"reviewer"} }),
	}
	target := Target{Profile: "implementer", Title: "Fix the web build with pnpm", Labels: []string{"web"}}

	all := Select(entries, target, 100_000)
	assert.Equal(t, []string{"pinned-a", "pnpm", "billing", "unrelated"}, all.IDs(),
		"pinned first, then by relevance: tag+words, then the rest")
	assert.Zero(t, all.Dropped)

	head := ApproxTokens(blockHeader) + ApproxTokens(line(entries[0]))
	pnpmCost := ApproxTokens(line(entries[2]))
	tight := Select(entries, target, head+pnpmCost)
	assert.Equal(t, []string{"pinned-a", "pnpm"}, tight.IDs(), "the budget keeps the best-ranked")
	assert.Equal(t, 2, tight.Dropped, "the lowest-ranked are dropped first")
	assert.LessOrEqual(t, tight.Tokens, head+pnpmCost)
	assert.False(t, tight.OverBudget)

	// One token short of pnpm: it is dropped, and so is everything ranked
	// below it, even an entry that would still fit.
	short := Select(append(entries, entry("tiny", "x")), target, head+pnpmCost-1)
	assert.Equal(t, []string{"pinned-a"}, short.IDs())

	none := Select(entries, target, 1)
	assert.Equal(t, []string{"pinned-a"}, none.IDs(), "pinned survives a budget it alone exceeds")
	assert.True(t, none.OverBudget)

	block := tight.Block()
	require.True(t, strings.HasPrefix(block, "## Memory\n"))
	assert.Contains(t, block, "- [fact] Keep PR descriptions under ten lines. (pinned-a)")
	for _, out := range []string{"(proposed)", "(archived)", "(reviewer-only)"} {
		assert.NotContains(t, all.Block(), out)
	}
	rev := Select(entries, Target{Profile: "reviewer"}, 100_000)
	assert.Contains(t, rev.IDs(), "reviewer-only", "a profile-scoped entry reaches its profile")
	assert.Empty(t, Select(nil, target, 100).Block())
}

// TestMemorySelectProjectWinsIDClash (#202): an id in both files counts
// once, the project copy winning.
func TestMemorySelectProjectWinsIDClash(t *testing.T) {
	priv := entry("same", "private copy", func(e *Entry) { e.Scope = ScopePrivate })
	proj := entry("same", "project copy")
	sel := Select([]Entry{priv, proj}, Target{}, 1000)
	require.Len(t, sel.Entries, 1)
	assert.Equal(t, "project copy", sel.Entries[0].Text)
}
