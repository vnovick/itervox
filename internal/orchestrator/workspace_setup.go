package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/workspace"
)

// #250 — workspace setup in a run: what workspace.copy_files did, and the
// workspace's port (ITERVOX_PORT) in the agent's and hooks' environment.
//
// Nothing here touches State. The port is owned by the workspace provider (a
// synchronised workspace-level ledger, see internal/workspace/ports.go): it
// is assigned by EnsureWorkspace on the worker goroutine and released by
// RemoveWorkspace, both of which already run off the event loop.

// copyFilesShown bounds how many paths one copy_files log line names.
const copyFilesShown = 10

// reportCopyFiles writes what workspace.copy_files did for this run to the
// daemon log and the issue's log. Worker goroutine; no State access.
func (o *Orchestrator) reportCopyFiles(identifier, runLogID string, ws workspace.Workspace) {
	add := func(level, msg string) {
		if o.logBuf != nil {
			o.logBuf.Add(identifier, makeBufLineWithSession(level, msg, runLogID))
		}
	}
	if ws.CopyFilesErr != nil {
		slog.Warn("worker: copy_files did not run", "issue_identifier", identifier, "error", ws.CopyFilesErr)
		add("WARN", fmt.Sprintf("worker: copy_files did not run: %v", ws.CopyFilesErr))
		return
	}
	rep := ws.CopyFiles
	if rep == nil {
		return
	}
	if len(rep.Copied) > 0 || len(rep.Kept) > 0 {
		msg := fmt.Sprintf("worker: copy_files copied %d file(s) (%d bytes)%s", len(rep.Copied), rep.Bytes, namedPaths(rep.Copied))
		if len(rep.Kept) > 0 {
			msg += fmt.Sprintf("; kept %d newer worktree copy(ies)", len(rep.Kept))
		}
		slog.Info("worker: copy_files", "issue_identifier", identifier,
			"copied", len(rep.Copied), "kept", len(rep.Kept), "bytes", rep.Bytes)
		add("INFO", msg)
	}
	if rep.CapHit {
		slog.Warn("worker: copy_files size cap reached; some files were not copied",
			"issue_identifier", identifier, "bytes", rep.Bytes)
		add("WARN", fmt.Sprintf("worker: copy_files size cap reached after %d bytes; raise workspace.copy_files_max_bytes to copy the rest", rep.Bytes))
	}
	for i, s := range rep.Skipped {
		if i == copyFilesShown {
			add("WARN", fmt.Sprintf("worker: copy_files skipped %d more", len(rep.Skipped)-copyFilesShown))
			break
		}
		slog.Warn("worker: copy_files skipped a match", "issue_identifier", identifier, "match", s)
		add("WARN", "worker: copy_files skipped "+s)
	}
	if len(rep.Unmatched) > 0 {
		add("INFO", "worker: copy_files matched nothing for "+strings.Join(rep.Unmatched, ", "))
	}
}

// namedPaths renders up to copyFilesShown paths as ": a, b, …".
func namedPaths(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	shown := paths[:min(len(paths), copyFilesShown)]
	s := ": " + strings.Join(shown, ", ")
	if len(paths) > len(shown) {
		s += ", …"
	}
	return s
}

// withLocalWorkspacePort puts ITERVOX_PORT in a local agent's environment
// through the run environment, which the runner applies without changing
// how it starts the CLI. An SSH worker gets it from withRemoteWorkspacePort.
func withLocalWorkspacePort(ctx context.Context, port int, workerHost string) context.Context {
	if port <= 0 || workerHost != "" {
		return ctx
	}
	env := agent.RunEnvFrom(ctx)
	env.Vars = append(slices.Clone(env.Vars), workspace.PortEnv(port)...)
	return agent.WithRunEnv(ctx, env)
}

// withRemoteWorkspacePort returns the turn's command for an SSH worker with
// an ITERVOX_PORT assignment in front: the local environment does not reach
// the remote shell. Only the turn's command line carries it; the command
// recorded for a later resume stays as configured.
func withRemoteWorkspacePort(agentCommand string, port int, workerHost string) string {
	if port <= 0 || workerHost == "" {
		return agentCommand
	}
	return prependEnvToCommand(agentCommand, map[string]string{workspace.PortEnvVar: strconv.Itoa(port)})
}

// runPortBinding is `{{ run.port }}`: the port, or "" without one.
func runPortBinding(port int) any {
	if port <= 0 {
		return ""
	}
	return port
}

// setRunPort sets run.port in the Liquid `run` object runBindings built.
func setRunPort(bindings map[string]any, port int) {
	if run, ok := bindings["run"].(map[string]any); ok {
		run["port"] = runPortBinding(port)
	}
}

// WorkspacePort returns identifier's workspace port (#250), or 0. Safe from
// any goroutine: it asks the workspace provider, which synchronises its own
// ledger; it reads no State and no cfgMu-guarded field.
func (o *Orchestrator) WorkspacePort(identifier string) int {
	if pp, ok := o.workspace.(workspace.PortProvider); ok {
		return pp.WorkspacePort(identifier)
	}
	return 0
}
