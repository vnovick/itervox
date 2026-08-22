package linear

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidIssueRef pins the two shapes Linear accepts. The live failure that
// motivated this sent seeded ids like "demo-id-1" into the batched
// `issues(filter: {id: {in: […]}})` query, which validates the whole list and
// rejected the ENTIRE request — taking down the one real UUID alongside ten
// malformed ones.
func TestValidIssueRef(t *testing.T) {
	assert.True(t, validIssueRef("4e65c1a1-42ad-44dc-938f-5591ea50f744"), "UUID")
	assert.True(t, validIssueRef("TEAM-123"), "issue identifier")
	assert.True(t, validIssueRef("ENG-1"))
	assert.True(t, validIssueRef("DEMO-10"))

	// The exact shape from the live log.
	assert.False(t, validIssueRef("demo-id-1"), "seeded id Linear rejects")
	assert.False(t, validIssueRef(""))
	assert.False(t, validIssueRef("not-a-uuid"))
	assert.False(t, validIssueRef("TEAM-"), "identifier needs a number")
	assert.False(t, validIssueRef("-123"), "identifier needs a team prefix")
	assert.False(t, validIssueRef("4e65c1a1-42ad-44dc-938f"), "truncated UUID")
}

// TestPartitionValidIssueRefs is the completeness proof: every ref lands in
// exactly one bucket, and the buckets sum to the input — so filtering cannot
// silently drop a healthy id.
func TestPartitionValidIssueRefs(t *testing.T) {
	refs := []string{
		"demo-id-1", "4e65c1a1-42ad-44dc-938f-5591ea50f744", "ENG-7", "demo-id-2",
	}
	valid, invalid := partitionValidIssueRefs(refs)

	assert.Equal(t, []string{"4e65c1a1-42ad-44dc-938f-5591ea50f744", "ENG-7"}, valid)
	assert.Equal(t, []string{"demo-id-1", "demo-id-2"}, invalid)
	assert.Len(t, refs, len(valid)+len(invalid), "buckets must sum to the input")
}
