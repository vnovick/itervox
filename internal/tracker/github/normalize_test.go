package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawGHIssue builds a production-shaped raw GitHub REST API issue map (as
// decoded by encoding/json into map[string]any — numbers arrive as
// float64), the same shape normalizeIssue is called with in client.go.
func rawGHIssue(number int, body string) map[string]any {
	return map[string]any{
		"number":     float64(number),
		"title":      "Test issue",
		"state":      "open",
		"labels":     []any{},
		"body":       body,
		"created_at": "2024-01-01T00:00:00Z",
		"updated_at": "2024-01-01T00:00:00Z",
	}
}

// TestExtractBlockersPhraseTable pins each of the seven phrases from the
// spec's GitHub body patterns section as producing a blocker edge.
func TestExtractBlockersPhraseTable(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"blocked by", "This is blocked by #5."},
		{"blocked on", "This is blocked on #5."},
		{"depends on", "This depends on #5."},
		{"depends upon", "This depends upon #5."},
		{"requires", "This requires #5."},
		{"waiting on", "This is waiting on #5."},
		{"waiting for", "This is waiting for #5."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue := normalizeIssue(rawGHIssue(1, tc.body), "todo")
			require.NotNil(t, issue)
			require.Len(t, issue.BlockedBy, 1)
			require.NotNil(t, issue.BlockedBy[0].ID)
			require.NotNil(t, issue.BlockedBy[0].Identifier)
			assert.Equal(t, "5", *issue.BlockedBy[0].ID)
			assert.Equal(t, "#5", *issue.BlockedBy[0].Identifier)
		})
	}
}

// TestExtractBlockersReferenceLists pins the reference-list grammar: #N
// tokens separated by whitespace, commas, "and", or "&", stopping at the
// first token that is neither a separator nor a #N reference.
func TestExtractBlockersReferenceLists(t *testing.T) {
	t.Run("comma and and separated", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "Depends on #3, #4 and #5."), "todo")
		require.NotNil(t, issue)
		require.Len(t, issue.BlockedBy, 3)
		assert.Equal(t, "#3", *issue.BlockedBy[0].Identifier)
		assert.Equal(t, "#4", *issue.BlockedBy[1].Identifier)
		assert.Equal(t, "#5", *issue.BlockedBy[2].Identifier)
	})

	t.Run("whitespace separated", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "Depends on #3 #4."), "todo")
		require.NotNil(t, issue)
		require.Len(t, issue.BlockedBy, 2)
	})

	t.Run("comma separated no spaces", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "Depends on #3,#4,#5."), "todo")
		require.NotNil(t, issue)
		require.Len(t, issue.BlockedBy, 3)
	})

	t.Run("stops at prose after first ref", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "depends on #3 and also cleanup"), "todo")
		require.NotNil(t, issue)
		require.Len(t, issue.BlockedBy, 1)
		assert.Equal(t, "#3", *issue.BlockedBy[0].Identifier)
	})
}

// TestExtractBlockersDocumentedAcceptance pins the deliberate-acceptance
// decision from docs/superpowers/specs/2026-08-05-tracker-edge-widening-design.md
// ("Deliberate acceptance: casual phrasing like 'requires #5 to be reviewed'
// WILL match; pinned in tests as documented behavior") — this is intentional
// over-capture, not a bug: casual phrasing after "requires" still creates a
// blocker edge.
func TestExtractBlockersDocumentedAcceptance(t *testing.T) {
	issue := normalizeIssue(rawGHIssue(1, "This requires #5 to be reviewed before merge."), "todo")
	require.NotNil(t, issue)
	require.Len(t, issue.BlockedBy, 1)
	assert.Equal(t, "#5", *issue.BlockedBy[0].Identifier)
}

// TestExtractBlockersColonForm pins the widened colon-form grammar (wave-2
// polish plan Task 3, item 1 / gh issue #53 deferral "Colon-form capture
// gap"): after the phrase, an optional ":" may precede the reference list,
// and the list may continue across newlines onto bullet-shaped
// ("- #N" / "* #N", optionally indented) lines. A blank line stops the list.
func TestExtractBlockersColonForm(t *testing.T) {
	t.Run("colon then single ref on same line", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "Depends on: #3"), "todo")
		require.NotNil(t, issue)
		require.Len(t, issue.BlockedBy, 1)
		assert.Equal(t, "#3", *issue.BlockedBy[0].Identifier)
	})

	t.Run("colon then bullet list across newlines", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "Depends on:\n- #3\n- #4"), "todo")
		require.NotNil(t, issue)
		require.Len(t, issue.BlockedBy, 2)
		assert.Equal(t, "#3", *issue.BlockedBy[0].Identifier)
		assert.Equal(t, "#4", *issue.BlockedBy[1].Identifier)
	})

	t.Run("colon then asterisk bullet list", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "Depends on:\n* #3\n* #4"), "todo")
		require.NotNil(t, issue)
		require.Len(t, issue.BlockedBy, 2)
	})

	t.Run("colon then blank line stops the list", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "Depends on:\n\n#3"), "todo")
		require.NotNil(t, issue)
		assert.Empty(t, issue.BlockedBy)
	})

	t.Run("lowercase colon with comma list", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "depends on: #3, #4"), "todo")
		require.NotNil(t, issue)
		require.Len(t, issue.BlockedBy, 2)
		assert.Equal(t, "#3", *issue.BlockedBy[0].Identifier)
		assert.Equal(t, "#4", *issue.BlockedBy[1].Identifier)
	})
}

// TestExtractBlockersCrossRepoMidListSkip pins the widened cross-repo
// mid-list grammar (Task 3, item 2 / gh issue #53 deferral "Cross-repo
// mid-list ref"): an "owner/repo#N" token mid-list is consumed and skipped
// rather than terminating the reference list.
func TestExtractBlockersCrossRepoMidListSkip(t *testing.T) {
	issue := normalizeIssue(rawGHIssue(1, "depends on #3, foo/bar#4, #5"), "todo")
	require.NotNil(t, issue)
	require.Len(t, issue.BlockedBy, 2)
	assert.Equal(t, "#3", *issue.BlockedBy[0].Identifier)
	assert.Equal(t, "#5", *issue.BlockedBy[1].Identifier)
}

// TestExtractBlockersAdversarialRegression pins the adversarial cases
// surfaced in the earlier tracker-edge-widening review, so the colon-form
// and cross-repo-skip grammar changes above cannot regress them.
func TestExtractBlockersAdversarialRegression(t *testing.T) {
	t.Run("trailing period after single ref", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "blocked by #3."), "todo")
		require.NotNil(t, issue)
		require.Len(t, issue.BlockedBy, 1)
		assert.Equal(t, "#3", *issue.BlockedBy[0].Identifier)
	})

	t.Run("phrase with dangling hash and no digits", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "requires #"), "todo")
		require.NotNil(t, issue)
		assert.Empty(t, issue.BlockedBy)
	})

	t.Run("phrase at end of body with nothing following", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "This requires"), "todo")
		require.NotNil(t, issue)
		assert.Empty(t, issue.BlockedBy)
	})

	t.Run("double hash does not match", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "depends on ##5"), "todo")
		require.NotNil(t, issue)
		assert.Empty(t, issue.BlockedBy)
	})

	t.Run("hash-digit-letter has no boundary", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "depends on #5x"), "todo")
		require.NotNil(t, issue)
		assert.Empty(t, issue.BlockedBy)
	})

	t.Run("and with no surrounding whitespace has no boundary", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "depends on #3and#4"), "todo")
		require.NotNil(t, issue)
		assert.Empty(t, issue.BlockedBy)
	})

	t.Run("cross-repo ref alone still yields no ref", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "Depends on foo/bar#7."), "todo")
		require.NotNil(t, issue)
		assert.Empty(t, issue.BlockedBy)
	})
}

// TestExtractBlockersNegatives pins the cases that must NOT produce an edge.
func TestExtractBlockersNegatives(t *testing.T) {
	t.Run("bare reference without phrase", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "See #5 for context."), "todo")
		require.NotNil(t, issue)
		assert.Empty(t, issue.BlockedBy)
	})

	t.Run("phrase without a numeric reference", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "This is blocked by someone else."), "todo")
		require.NotNil(t, issue)
		assert.Empty(t, issue.BlockedBy)
	})

	t.Run("self reference is dropped", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(7, "This depends on #7 and #8."), "todo")
		require.NotNil(t, issue)
		require.Len(t, issue.BlockedBy, 1)
		assert.Equal(t, "#8", *issue.BlockedBy[0].Identifier)
	})

	t.Run("cross-repo reference is ignored", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "Depends on foo/bar#7."), "todo")
		require.NotNil(t, issue)
		assert.Empty(t, issue.BlockedBy)
	})

	t.Run("duplicate references across phrases are deduped", func(t *testing.T) {
		issue := normalizeIssue(rawGHIssue(1, "Blocked by #5. Also requires #5 again."), "todo")
		require.NotNil(t, issue)
		require.Len(t, issue.BlockedBy, 1)
		assert.Equal(t, "#5", *issue.BlockedBy[0].Identifier)
	})
}
