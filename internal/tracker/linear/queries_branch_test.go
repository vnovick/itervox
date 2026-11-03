package linear

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBlockerSelectionsIncludeBranchName (#103): every relation and
// sub-issue selection asks for the blocker's branchName.
func TestBlockerSelectionsIncludeBranchName(t *testing.T) {
	sel := regexp.MustCompile(`(issue|nodes) \{ id identifier url[^}]*state \{ name \} \}`)
	for name, q := range map[string]string{
		"QueryCandidateIssues": QueryCandidateIssues, "QueryIssueDetail": QueryIssueDetail,
		"QueryCandidateIssuesAll": QueryCandidateIssuesAll, "QueryCandidateIssuesNoProject": QueryCandidateIssuesNoProject,
		"QueryIssueDetailsByIDs": QueryIssueDetailsByIDs, "QueryIssuesByIDs": QueryIssuesByIDs,
	} {
		matches := sel.FindAllString(q, -1)
		assert.NotEmpty(t, matches, name)
		for _, m := range matches {
			assert.Contains(t, m, "branchName", "%s: %s", name, m)
		}
	}
}
