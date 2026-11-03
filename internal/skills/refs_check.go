package skills

import (
	"fmt"
	"strings"
)

// Per-occurrence reference checking (#123): the same resolution and issues
// as ValidateProfileRefs (#86), reported for every occurrence with its
// position so the dashboard's prompt editor and `itervox doctor` agree.

// Reference statuses reported by CheckPromptRefs.
const (
	RefStatusOK          = "ok"           // resolves for the backend
	RefStatusMissing     = "missing"      // MISSING_SKILL_REF / MISSING_SUBAGENT_REF
	RefStatusOutsideRepo = "outside_repo" // USER_SCOPE_REF_ON_SSH
)

// refResolver checks references of one profile against the inventory
// entries its backend can use.
type refResolver struct {
	profile, backend string
	sshHosts         []string
	skills, agents   map[string]*refTarget
}

func newRefResolver(inv *Inventory, profile, backend string, sshHosts []string) refResolver {
	skillIdx, agentIdx := resolvableNames(inv, backend)
	return refResolver{profile: profile, backend: backend, sshHosts: sshHosts, skills: skillIdx, agents: agentIdx}
}

// check returns the reference's status and, unless it is RefStatusOK, the
// issue doctor reports for it.
func (r refResolver) check(ref PromptRef) (string, *InventoryIssue) {
	idx := r.skills
	if ref.Kind == "subagent" {
		idx = r.agents
	}
	target := idx[strings.ToLower(ref.Name)]
	if target == nil {
		issue := missingRefIssue(r.profile, r.backend, ref)
		return RefStatusMissing, &issue
	}
	if len(r.sshHosts) > 0 && target.onlyOutsideRepo() {
		return RefStatusOutsideRepo, &InventoryIssue{
			ID:       "USER_SCOPE_REF_ON_SSH",
			Severity: "info",
			Title:    fmt.Sprintf("Profile %q references %s %q that only exists outside the repository", r.profile, ref.Kind, ref.Name),
			Description: fmt.Sprintf("%s %q was found only in user or plugin scope on this machine. Agents on SSH hosts (%s) see the repository but not this machine's home directory or plugins, so the %s may be missing there. %s",
				capitalize(ref.Kind), ref.Name, strings.Join(r.sshHosts, ", "), ref.Kind, sshScopeRemedy(r.backend, ref.Kind)),
			Affected: []string{r.profile, ref.Name},
		}
	}
	return RefStatusOK, nil
}

// RefFinding is one reference occurrence in a prompt and what the #86
// validator says about it. Issue is nil when Status is RefStatusOK.
type RefFinding struct {
	PromptRefSpan
	Status string
	Issue  *InventoryIssue
}

// CheckPromptRefs runs the doctor validator (ValidateProfileRefs) over one
// prompt text for a profile on backend and returns every reference
// occurrence with its byte range, so the dashboard editor can underline
// exactly what doctor reports (#123). profile only appears in messages.
func CheckPromptRefs(inv *Inventory, profile, backend, text string, sshHosts []string) []RefFinding {
	if inv == nil {
		return nil
	}
	r := newRefResolver(inv, profile, backend, sshHosts)
	var out []RefFinding
	for _, span := range FindPromptRefs(text) {
		status, issue := r.check(span.PromptRef)
		out = append(out, RefFinding{PromptRefSpan: span, Status: status, Issue: issue})
	}
	return out
}
