package agent

import (
	"os"
	"strings"
)

// itervoxAgentMarker is exported into every agent turn's environment.
//
// Its purpose is to let a TARGET repository's PreToolUse hook tell an itervox
// run apart from an interactive session. Repos that deny `git commit` /
// `git push` by default would otherwise block the daemon's own workers, whose
// entire job is to commit a branch and open a PR — and a deny hook, unlike a
// deny rule, is not bypassed by --dangerously-skip-permissions.
//
// SECURITY: this is a convenience marker, NOT authentication. Any process can
// set it. A repo owner uses it to express "I trust itervox runs"; it proves
// nothing on its own. Branch protection on the remote is what actually makes a
// forced or protected-branch push impossible.
//
// Distinct from ITERVOX_RUN_ID (internal/orchestrator/worker.go), which is the
// action-bridge run handle and is set only when that bridge is live.
const itervoxAgentMarker = "ITERVOX_AGENT=1"

// itervoxAgentEnv returns the environment for a locally-executed agent turn:
// the daemon's own environment, the itervox marker, and any extras. Empty
// extras are dropped so a conditionally-unset value cannot become a bare "="
// entry.
//
// Every local exec.CommandContext in this package must use this. Go inherits
// the parent environment when cmd.Env is nil, so a path that simply omits the
// assignment silently ships without the marker.
func itervoxAgentEnv(extra ...string) []string {
	env := append(os.Environ(), itervoxAgentMarker)
	for _, kv := range extra {
		if strings.TrimSpace(kv) == "" {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// itervoxAgentExportPrefix returns the `export …; ` prefix that carries the
// marker across an SSH boundary, where cmd.Env applies to the local ssh
// process and does not reach the remote shell.
//
// Mirrors how CLAUDE_CODE_LOG_DIR is already carried remotely.
func itervoxAgentExportPrefix() string {
	return "export " + itervoxAgentMarker + "; "
}
