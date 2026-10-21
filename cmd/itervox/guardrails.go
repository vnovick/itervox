package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"

	"gopkg.in/yaml.v3"
)

// Guardrails wiring (#204). Dispatched runs are guarded by the orchestrator
// (internal/orchestrator/guardrails.go); this file adds the binary's shim
// and hook entrypoints, the server adapter for the guardrail endpoints, and
// the guardrails of tmux sessions started with "New session".

// runGuardrailEntrypoint runs a guardrail shim or hook and exits when args
// name one. It is the first thing main does: a shim forwards the agent's
// environment untouched (no dotenv load, no GIT_DIR scrub) and must never
// reset the agent's terminal, so it exits without the stty restore.
func runGuardrailEntrypoint(args []string) {
	if len(args) > 1 && guardrails.IsEntrypoint(args[1]) {
		exitWithoutTerminalRestore(guardrails.Main(args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
}

// guardrailsAdapter serves the guardrail endpoints from the orchestrator.
type guardrailsAdapter struct{ orch *orchestrator.Orchestrator }

func (a guardrailsAdapter) GuardrailReport(identifier, runID string, ev guardrails.Event) error {
	return a.orch.GuardrailReport(identifier, runID, ev)
}

func (a guardrailsAdapter) RequestGuardrailApproval(identifier, runID string, req guardrails.ApprovalRequest) (guardrails.ApprovalStatus, error) {
	st, err := a.orch.RequestGuardrailApproval(identifier, runID, req)
	return st, mapIssueControlErr(err)
}

func (a guardrailsAdapter) GuardrailApprovalStatus(identifier, runID, id string) guardrails.ApprovalStatus {
	return a.orch.GuardrailApprovalStatus(identifier, runID, id)
}

func (a guardrailsAdapter) ProvideAgentInput(identifier, message string) error {
	return mapIssueControlErr(a.orch.ProvideAgentInput(identifier, message))
}

// wireGuardrails prepares one run generation's guardrails: the metric
// series, a startup warning for what v1 does not cover, and the guardrails
// of tmux sessions. It returns the server client (nil without an action
// token store, i.e. without a listening server).
func wireGuardrails(cfg *config.Config, orch *orchestrator.Orchestrator, tmux *tmuxSessions, tokens *agentactions.Store, addr string) server.GuardrailsClient {
	metrics.PreinitGuardrailBlocks(guardrails.Kinds...)
	if !cfg.Guardrails.Enabled {
		return guardrailsAdapter{orch: orch}
	}
	if len(cfg.Agent.SSHHosts) > 0 {
		slog.Warn("guardrails: runs on SSH worker hosts are not guarded (v1 guards local runs only)",
			"ssh_hosts", len(cfg.Agent.SSHHosts))
	}
	if tmux != nil {
		exe, err := os.Executable()
		if err != nil {
			slog.Warn("guardrails: cannot resolve the itervox binary; tmux sessions are unguarded", "error", err)
		} else {
			daemonURL := ""
			if addr != "" {
				daemonURL = agentActionBaseURL(addr)
			}
			tmux.guard = newTmuxGuardrails(cfg, exe, tokens, daemonURL)
		}
	}
	return guardrailsAdapter{orch: orch}
}

// tmuxGuardTokenTTL bounds a tmux session's guardrail token (reports
// only: tmux sessions have no approval channel).
const tmuxGuardTokenTTL = 24 * time.Hour

// tmuxGuardrails prepares the guardrails of the tmux sessions itervox
// starts. The issue branch of a session is the branch checked out in its
// folder when it starts (none on a protected branch: then the agent may
// only push a new branch it creates). Deploy commands are refused: no
// human can be asked through the dashboard for a session that is not an
// issue run, and the operator can run them in the session themselves.
type tmuxGuardrails struct {
	cfg       config.GuardrailsConfig
	protected []string
	exe       string
	tokens    *agentactions.Store
	daemonURL string

	mu       sync.Mutex
	runtimes map[string]tmuxGuard
}

type tmuxGuard struct {
	rt    *guardrails.Runtime
	token string
}

func newTmuxGuardrails(cfg *config.Config, exe string, tokens *agentactions.Store, daemonURL string) *tmuxGuardrails {
	return &tmuxGuardrails{cfg: cfg.Guardrails, protected: guardrails.ProtectedBranches(cfg.Agent.BaseBranch, cfg.Workspace.BaseBranch),
		exe: exe, tokens: tokens, daemonURL: daemonURL, runtimes: map[string]tmuxGuard{}}
}

// prepare returns the command prefix of session id in cwd.
func (g *tmuxGuardrails) prepare(ctx context.Context, id, cwd string) (string, error) {
	opts := guardrails.Options{
		Executable:        g.exe,
		WorkDir:           cwd,
		Identifier:        id,
		IssueBranch:       checkedOutBranch(ctx, cwd),
		ProtectedBranches: g.protected,
		AllowForcePush:    g.cfg.AllowForcePush,
		ApprovalCommands:  g.cfg.ApprovalCommands,
	}
	token := ""
	if g.tokens != nil && g.daemonURL != "" {
		if t, err := g.tokens.IssueScoped(id, id, []string{guardrails.TokenAction}, "", "", tmuxGuardTokenTTL); err == nil {
			token = t
			opts.DaemonURL, opts.Token = g.daemonURL, t
		}
	}
	rt, err := guardrails.Prepare(ctx, opts)
	if err != nil {
		g.tokens.Revoke(token)
		return "", err
	}
	g.mu.Lock()
	g.runtimes[id] = tmuxGuard{rt: rt, token: token}
	g.mu.Unlock()
	return rt.CommandPrefix(), nil
}

// release removes session id's guardrails (the session was killed or
// never started). A session that ends on its own keeps its directory
// until the daemon's temp directory is cleaned: it may outlive this
// daemon, and its shims must keep refusing rather than vanish.
func (g *tmuxGuardrails) release(id string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	tg, ok := g.runtimes[id]
	delete(g.runtimes, id)
	g.mu.Unlock()
	if ok {
		g.tokens.Revoke(tg.token)
		tg.rt.Remove()
	}
}

// checkedOutBranch is the branch checked out in dir ("" when detached or
// not a repository).
func checkedOutBranch(ctx context.Context, dir string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := gitexec.Command(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// withoutGuardrailApprovals drops the guardrail approval entries from an
// input-required map: they are not questions an automation may answer.
func withoutGuardrailApprovals(m map[string]*orchestrator.InputRequiredEntry) map[string]*orchestrator.InputRequiredEntry {
	out := maps.Clone(m)
	maps.DeleteFunc(out, func(_ string, e *orchestrator.InputRequiredEntry) bool {
		return e != nil && e.GuardrailApprovalID != ""
	})
	return out
}

// guardrailsHeadComment introduces the guardrails: block in a workflow.
const guardrailsHeadComment = `Guardrails (#204): agents push only to their issue branch (no force
pushes, deletions or tags unless allow_force_push), never merge their own
PR, and wait for a human approval before an approval_commands command runs.
Guidance, not a sandbox.`

// writeGuardrailsBlock writes the guardrails: block of a new workflow.
func writeGuardrailsBlock(b *strings.Builder) {
	b.WriteString("\n")
	for _, line := range strings.Split(guardrailsHeadComment, "\n") {
		b.WriteString("# " + line + "\n")
	}
	b.WriteString("guardrails:\n  enabled: true\n  allow_force_push: false\n  approval_commands:\n")
	for _, c := range config.DefaultGuardrailApprovalCommands {
		b.WriteString("    - " + strconv.Quote(c) + "\n")
	}
}

// appendGuardrailsBlock adds the default guardrails: block to a workflow's
// front matter (itervox init --update).
func appendGuardrailsBlock(root *yaml.Node) {
	cmds := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, c := range config.DefaultGuardrailApprovalCommands {
		n := yamlStringNode(c)
		n.Style = yaml.DoubleQuotedStyle
		cmds.Content = append(cmds.Content, n)
	}
	block := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		yamlStringNode("enabled"), {Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
		yamlStringNode("allow_force_push"), {Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"},
		yamlStringNode("approval_commands"), cmds,
	}}
	key := yamlStringNode("guardrails")
	key.HeadComment = guardrailsHeadComment
	root.Content = append(root.Content, key, block)
}

// appendGuardrailsText adds the default guardrails: block at the end of a
// workflow's front matter as text, leaving every other byte as it was.
func appendGuardrailsText(workflowPath, front, body string) error {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(front)
	if front != "" && !strings.HasSuffix(front, "\n") {
		b.WriteByte('\n')
	}
	writeGuardrailsBlock(&b)
	b.WriteString("---\n")
	b.WriteString(body)
	if err := atomicfs.WriteFile(workflowPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("itervox init --update: write %s: %w", workflowPath, err)
	}
	return nil
}
