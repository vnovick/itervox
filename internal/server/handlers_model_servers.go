package server

import (
	"net/http"

	"github.com/vnovick/itervox/internal/apitokens"
)

// ModelServerRow is one model server a profile command points its agent at
// (`ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude`), as
// GET /api/v1/model-servers lists it. The daemon never probes it; the
// desktop app does.
type ModelServerRow struct {
	Var string `json:"var"`
	URL string `json:"url"`
	// Profiles are the run modes using it; "(default)" is agent.command.
	Profiles []string `json:"profiles"`
	// Pools are the quota pools of those profiles (agent.profiles.<name>.pool).
	Pools []string `json:"pools"`
	// Remote is set when agent.ssh_hosts is configured: runs go to worker
	// hosts, so the URL names a server on those hosts, not on this machine.
	Remote bool `json:"remote"`
	// Unchecked is set when the URL comes from a shell variable ($VAR).
	Unchecked bool `json:"unchecked,omitempty"`
}

// ModelServerLister is optionally implemented by the orchestrator client:
// it lists the model servers of the current profile configuration.
type ModelServerLister interface {
	ModelServers() []ModelServerRow
}

// handleModelServers lists the model servers profile commands point at.
// GET /api/v1/model-servers (viewer). Read-only, no probing. Below admin a
// URL's credentials and known secret shapes are redacted, as profile
// commands are on the other profile routes.
func (s *Server) handleModelServers(w http.ResponseWriter, r *http.Request) {
	rows := []ModelServerRow{}
	if l, ok := s.client.(ModelServerLister); ok {
		rows = append(rows, l.ModelServers()...)
	}
	admin := callerScope(r) == apitokens.ScopeAdmin
	for i := range rows {
		if rows[i].Profiles == nil {
			rows[i].Profiles = []string{}
		}
		if rows[i].Pools == nil {
			rows[i].Pools = []string{}
		}
		if !admin {
			rows[i].URL = redactSecretString(rows[i].URL)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": rows})
}
