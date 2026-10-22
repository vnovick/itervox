package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// MemberRecord is what the hub knows about a member from the daemon
// registry (cmd/itervox's daemonRecord, filtered to hub members).
type MemberRecord struct {
	PID        int
	Workflow   string
	ProjectDir string
	// URL is the member's dashboard URL, e.g. http://127.0.0.1:8090/.
	URL     string
	Version string
	// StartedAt is the member process's start, RFC 3339.
	StartedAt string
	// TokenRequired is false for a member running with
	// server.allow_unauthenticated: it is listed and its feed merged, but
	// it is never proxied (the hub could not cap a caller's scope on it).
	TokenRequired bool
}

// startedAt parses StartedAt; zero when it does not parse.
func (r MemberRecord) startedAt() time.Time {
	t, err := time.Parse(time.RFC3339, r.StartedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// sameProcess reports whether a and b describe the same member process.
func (r MemberRecord) sameProcess(o MemberRecord) bool {
	return r.PID == o.PID && r.StartedAt == o.StartedAt && r.URL == o.URL && r.Workflow == o.Workflow
}

// projectSlug turns a project directory into a URL-safe project name:
// lowercase letters, digits, '.', '_' and '-', never starting with a dot.
func projectSlug(dir string) string {
	base := strings.ToLower(filepath.Base(filepath.Clean(dir)))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(strings.TrimLeft(b.String(), "."), "-")
	if name == "" {
		return "project"
	}
	return name
}

// workflowHash is a short, stable digest of a workflow path.
func workflowHash(workflow string) string {
	sum := sha256.Sum256([]byte(workflow))
	return hex.EncodeToString(sum[:3])
}

// assignNames gives every record a unique project name, by workflow path:
// the directory's slug when no other member shares it, otherwise the slug
// plus a short hash of the workflow path. The suffix depends only on the
// path, so a name never moves to another project when members come and go.
func assignNames(records []MemberRecord) map[string]string {
	bySlug := map[string][]string{}
	for _, r := range records {
		s := projectSlug(r.ProjectDir)
		if !slices.Contains(bySlug[s], r.Workflow) {
			bySlug[s] = append(bySlug[s], r.Workflow)
		}
	}
	names := make(map[string]string, len(records))
	for slug, workflows := range bySlug {
		for _, wf := range workflows {
			if len(workflows) == 1 {
				names[wf] = slug
			} else {
				names[wf] = slug + "-" + workflowHash(wf)
			}
		}
	}
	return names
}

// dedupeByWorkflow keeps one record per workflow (the newest start), so a
// stale record of a restarted member never shadows the live one.
func dedupeByWorkflow(records []MemberRecord) []MemberRecord {
	out := make([]MemberRecord, 0, len(records))
	for _, r := range records {
		i := slices.IndexFunc(out, func(o MemberRecord) bool { return o.Workflow == r.Workflow })
		switch {
		case i < 0:
			out = append(out, r)
		case r.startedAt().After(out[i].startedAt()):
			out[i] = r
		}
	}
	slices.SortFunc(out, func(a, b MemberRecord) int { return strings.Compare(a.Workflow, b.Workflow) })
	return out
}

// ProjectNames returns the project name the hub gives each member, by
// workflow path (for `itervox hub`, which lists members without asking the
// hub).
func ProjectNames(records []MemberRecord) map[string]string {
	return assignNames(dedupeByWorkflow(records))
}
