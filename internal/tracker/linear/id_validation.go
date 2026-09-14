package linear

import "regexp"

// Linear accepts two ID shapes wherever an issue is referenced: a UUID, or a
// human issue identifier like TEAM-123. Anything else is rejected by argument
// validation.
var (
	uuidRe            = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	issueIdentifierRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)
)

// validIssueRef reports whether ref is a shape Linear will accept as an issue
// reference.
//
// This matters for BATCHED queries specifically. `issues(filter: {id: {in:
// [...]}})` validates the whole list and rejects the ENTIRE query if any
// element is malformed — so one stale or seeded row (e.g. "demo-id-1") takes
// down the request for every healthy row beside it. Observed live: a batch of
// 11 failed because 10 ids were malformed, including the one real UUID.
//
// Filtering here rather than in the orchestrator keeps Linear's ID rules in
// the Linear adapter. Callers see the malformed ids simply omitted from the
// response, which the dependency-audit refresh already handles by confirming
// each omission with a single-issue fetch — and the singular `issue(id:)`
// query DOES accept a malformed id, answering "Entity not found", which is
// what finally retires the row.
func validIssueRef(ref string) bool {
	return uuidRe.MatchString(ref) || issueIdentifierRe.MatchString(ref)
}

// partitionValidIssueRefs splits refs into those Linear will accept and those
// it will not. Every ref lands in exactly one bucket.
func partitionValidIssueRefs(refs []string) (valid, invalid []string) {
	for _, ref := range refs {
		if validIssueRef(ref) {
			valid = append(valid, ref)
			continue
		}
		invalid = append(invalid, ref)
	}
	return valid, invalid
}
