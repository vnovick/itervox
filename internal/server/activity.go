package server

import (
	"errors"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/sysmon"
)

// activity.go — system visibility: GET /api/v1/issues/{identifier}/activity
// (a run's activity audit) and the scope rules for the resource rows that
// ride on the snapshot and the run context.
//
// Scopes. The activity audit lists command lines, file paths and remote
// addresses: they are redacted at the source (logging.RedactString plus
// flag/env-value scrubbing) but still say a lot about the machine and the
// code, so the route is operator. The resource rows (CPU, memory, process
// counts) are viewer reads; their top child commands keep their names for
// viewers but lose their arguments below operator.

// CapabilitySystemVisibility is advertised in StateSnapshot.Capabilities
// when the daemon runs the run monitor: per-run resources, the system
// block and the activity endpoint.
const CapabilitySystemVisibility = "system_visibility"

// ActivityClient backs GET /api/v1/issues/{identifier}/activity.
// *sysmon.Monitor implements it; it only reads its own store and files.
type ActivityClient interface {
	Activity(identifier, runID string) (sysmon.ActivityReport, error)
}

// handleIssueActivity serves GET /api/v1/issues/{identifier}/activity[?run=].
func (s *Server) handleIssueActivity(w http.ResponseWriter, r *http.Request) {
	if s.activity == nil {
		writeError(w, http.StatusServiceUnavailable, "not_supported", "activity recording is not available")
		return
	}
	id := chi.URLParam(r, "identifier")
	rep, err := s.activity.Activity(id, r.URL.Query().Get("run"))
	switch {
	case errors.Is(err, sysmon.ErrActivityNotFound):
		writeError(w, http.StatusNotFound, "no_activity", "no activity is recorded for this issue or run")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "activity_failed", "the activity log could not be read")
		return
	}
	if rep.Notes == nil {
		rep.Notes = []string{}
	}
	if rep.Commands == nil {
		rep.Commands = []sysmon.CommandRecord{}
	}
	if rep.Files == nil {
		rep.Files = []sysmon.FileRecord{}
	}
	if rep.Connections == nil {
		rep.Connections = []sysmon.ConnRecord{}
	}
	writeJSON(w, http.StatusOK, rep)
}

// usageForScope returns u as a caller of scope may see it: below operator
// the top child commands keep their names but not their arguments.
func usageForScope(u *sysmon.RunUsage, scope apitokens.Scope) *sysmon.RunUsage {
	if u == nil || scope == "" || scope.Allows(apitokens.ScopeOperator) {
		return u
	}
	cp := *u
	cp.TopCommands = slices.Clone(u.TopCommands)
	for i := range cp.TopCommands {
		cp.TopCommands[i].Args = ""
	}
	return &cp
}

// runningForScope applies usageForScope to every running row (copying the
// slice: snapshots are shared between callers).
func runningForScope(rows []RunningRow, scope apitokens.Scope) []RunningRow {
	if scope == "" || scope.Allows(apitokens.ScopeOperator) {
		return rows
	}
	out := slices.Clone(rows)
	for i := range out {
		out[i].Resources = usageForScope(out[i].Resources, scope)
	}
	return out
}
