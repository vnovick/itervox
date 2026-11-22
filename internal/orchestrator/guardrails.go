package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/tracker"
)

// Guardrails (#204). A dispatched run gets a private runtime directory
// (internal/guardrails; on an SSH worker, installed there, guardrails/remote.go): PATH shims for git, gh, glab and the
// approval-command programs, and a pre-push hook through core.hooksPath.
// The shims and hook call the daemon's token-gated agent-actions endpoints
// (internal/server) to report refusals and to ask for approvals.
//
// An approval is shown to a human as an input-required entry for the issue
// (InputRequiredEntry.GuardrailApprovalID) while the run keeps going — the
// agent's command blocks in its shim. The dashboard, the Mac app and a
// tracker reply answer it through the usual input-required paths; the
// answer decides the request in o.guardApprovals and nothing is resumed.
// Agents cannot answer approvals: the agent-action provide-input route
// arrives with FromAgent set and is refused for these entries.

// EventGuardrailApproval raises an approval request to a human (#204).
const EventGuardrailApproval EventType = "GuardrailApproval"

// guardrailApprovalEvent is the payload of EventGuardrailApproval.
type guardrailApprovalEvent struct {
	ID      string
	RunID   string
	Command string
	// Lane, Paths and PathsTotal describe a push held by
	// guardrails.lanes.human (empty for an approval command).
	Lane       string
	Paths      []string
	PathsTotal int
}

// guardrailTokenTTL bounds a guardrail token: a run's turns plus slack, as
// for action tokens.
func guardrailTokenTTL(turnTimeoutMs, maxTurns int) time.Duration {
	return agentActionTokenTTL(turnTimeoutMs) * time.Duration(max(maxTurns, 1))
}

// protectedBranches are the branches no agent pushes to or merges into.
func protectedBranches(cfg *config.Config) []string {
	return guardrails.ProtectedBranches(cfg.Agent.BaseBranch, cfg.Workspace.BaseBranch)
}

// guardrailExecutable is the itervox binary shims run.
func (o *Orchestrator) guardrailExecutable() (string, error) {
	if o.guardrailExe != "" {
		return o.guardrailExe, nil
	}
	return os.Executable()
}

// SetGuardrailExecutable overrides the binary guardrail shims run (tests;
// production uses os.Executable). Must be called before Run.
func (o *Orchestrator) SetGuardrailExecutable(path string) { o.guardrailExe = path }

// runGuard is a run's prepared guardrails: a local runtime directory
// (*guardrails.Runtime) or one on an SSH worker (*guardrails.RemoteRuntime).
type runGuard interface {
	BinDir() string
	SetIssueBranch(branch string) error
	RefusedApprovals() []string
	Remove()
}

// prepareRunGuardrails prepares a run's guardrails and returns the
// environment the runner applies to the agent (agent.WithRunEnv), the
// guard and its token (nil/"" when the guardrails are off or preparation
// failed). prBase is the branch the issue's pull request targets: pushes
// are classified into guardrails.lanes against it. patchOut is push:
// daemon: the agent's environment then carries no forge credentials and its
// pushes are refused (a patch-out run whose guardrails cannot be prepared
// must not start; the worker checks).
//
// A local run whose guardrails cannot be prepared proceeds unguarded with
// a warning. A run on an SSH worker gets them installed on the worker
// (prepareRemoteRunGuardrails); when that fails the error is returned and
// the run does not start, unless guardrails.allow_unguarded_ssh. Worker
// goroutine; touches no State.
func (o *Orchestrator) prepareRunGuardrails(ctx context.Context, identifier, runID, wsPath, workerHost, branch, prBase string, turnTimeoutMs, maxTurns int, patchOut bool) (agent.RunEnv, runGuard, string, error) {
	g := o.cfg.Guardrails
	if !g.Enabled {
		return agent.RunEnv{}, nil, "", nil
	}
	exe, err := o.guardrailExecutable()
	if err != nil {
		if workerHost != "" {
			return o.remoteGuardrailsFailed(identifier, workerHost, fmt.Errorf("guardrails: cannot resolve the itervox binary: %w", err))
		}
		slog.Warn("guardrails: cannot resolve the itervox binary; run is unguarded", "issue_identifier", identifier, "error", err)
		return agent.RunEnv{}, nil, "", nil
	}
	opts := guardrails.Options{
		Executable:          exe,
		WorkDir:             wsPath,
		Identifier:          identifier,
		IssueBranch:         branch,
		ProtectedBranches:   protectedBranches(o.cfg),
		AllowForcePush:      g.AllowForcePush,
		ApprovalCommands:    g.ApprovalCommands,
		EnvApprovalCommands: guardrails.EnvApprovalsFromConfig(g.EnvApprovalCommands),
		ApprovalsEnabled:    true,
		Lanes:               guardrails.Lanes{Human: g.Lanes.Human, SecondOpinion: g.Lanes.SecondOpinion},
		BaseRefs:            reviewBaseCandidates(prBase, o.cfg.Agent.BaseBranch),
		PatchOut:            patchOut,
	}
	if patchOut {
		opts.StripEnv = o.patchOutStripEnv()
	}
	if o.oneShot != nil { // #252: approvals the operator gave this run
		opts.PreApproved = o.oneShot.PreApprovedCommands
	}
	token := ""
	if o.agentActionTokens != nil && o.agentActionBaseURL != "" {
		token, err = o.agentActionTokens.IssueScoped(identifier, runID, []string{guardrails.TokenAction}, "", "", guardrailTokenTTL(turnTimeoutMs, maxTurns))
		if err != nil {
			slog.Warn("guardrails: cannot issue a token; refusals are not reported and approvals are refused", "issue_identifier", identifier, "error", err)
			token = ""
		} else {
			opts.DaemonURL, opts.Token = o.agentActionBaseURL, token
		}
	}
	if workerHost != "" {
		return o.prepareRemoteRunGuardrails(ctx, identifier, workerHost, opts, token)
	}
	rt, err := guardrails.Prepare(ctx, opts)
	if err != nil {
		o.agentActionTokens.Revoke(token)
		slog.Warn("guardrails: cannot prepare; run is unguarded", "issue_identifier", identifier, "error", err)
		return agent.RunEnv{}, nil, "", nil
	}
	return rt.AgentEnv(), rt, token, nil
}

// prepareRemoteRunGuardrails installs a run's guardrails on its SSH worker
// (guardrails.InstallRemote): the shims and pre-push hook run there and
// call back over the turn's reverse-forwarded socket. Worker goroutine.
func (o *Orchestrator) prepareRemoteRunGuardrails(ctx context.Context, identifier, workerHost string, opts guardrails.Options, token string) (agent.RunEnv, runGuard, string, error) {
	forward := ""
	if token != "" {
		forward = forwardTarget(o.agentActionBaseURL)
	}
	rt, err := guardrails.InstallRemote(ctx, guardrails.RemoteOptions{Host: workerHost, Options: opts, ForwardTarget: forward, Token: token})
	if err != nil {
		o.agentActionTokens.Revoke(token)
		return o.remoteGuardrailsFailed(identifier, workerHost, err)
	}
	if token != "" && !rt.Callback() { // without a listener (itervox run) there was nothing to reach
		reason := "the daemon API URL is not plain http"
		if err := rt.CallbackErr(); err != nil {
			reason = logging.RedactString(err.Error())
		}
		slog.Warn("guardrails: run on an SSH worker is guarded, but its shims cannot reach the daemon: refusals are not reported and approval commands are refused",
			"issue_identifier", identifier, "worker_host", workerHost, "reason", reason)
		o.agentActionTokens.Revoke(token)
		token = ""
	}
	slog.Info("guardrails: installed on an SSH worker", "issue_identifier", identifier, "worker_host", workerHost, "dir", rt.Dir())
	return rt.AgentEnv(), rt, token, nil
}

// remoteGuardrailsFailed is the outcome of a run on an SSH worker whose
// guardrails could not be installed: an error (the run does not start), or
// with guardrails.allow_unguarded_ssh a warning and an unguarded run.
func (o *Orchestrator) remoteGuardrailsFailed(identifier, workerHost string, err error) (agent.RunEnv, runGuard, string, error) {
	if o.cfg.Guardrails.AllowUnguardedSSH {
		slog.Warn("guardrails: cannot install on the SSH worker; run is unguarded (guardrails.allow_unguarded_ssh)",
			"issue_identifier", identifier, "worker_host", workerHost, "error", logging.RedactString(err.Error()))
		return agent.RunEnv{}, nil, "", nil
	}
	return agent.RunEnv{}, nil, "", fmt.Errorf("%w; run not started (set guardrails.allow_unguarded_ssh to run it unguarded)", err)
}

// forwardTarget is the host:port of the daemon API URL that an SSH turn's
// reverse forward points at ("" when the URL is not plain http).
func forwardTarget(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.Port() == "" {
		return ""
	}
	return u.Host
}

// GuardrailReport records an operation a run's shim or hook refused: an
// slog line, itervox_guardrail_blocks_total{kind}, a RecentFailures entry
// and a line in the issue's log. The command is redacted again here.
// Safe from any goroutine.
func (o *Orchestrator) GuardrailReport(identifier, runID string, ev guardrails.Event) error {
	if !guardrails.IsKind(ev.Kind) {
		return fmt.Errorf("orchestrator: unknown guardrail kind %q", ev.Kind)
	}
	command := logging.RedactString(ev.Command)
	slog.Warn("guardrails: refused", "issue_identifier", identifier, "run_id", runID, "kind", ev.Kind,
		"tool", logging.RedactString(ev.Tool), "command", command)
	metrics.GuardrailBlock(ev.Kind)
	o.RecordFailure(FailureRecord{Kind: FailureKindGuardrail, Identifier: identifier, Source: ev.Kind,
		Message: "refused: " + command})
	if o.logBuf != nil {
		o.logBuf.Add(identifier, makeBufLineWithSession("WARN", "guardrails: refused "+ev.Kind+": "+command, runID))
	}
	return nil
}

// RequestGuardrailApproval raises (or joins) an approval request for the
// run's command. Safe from any goroutine: the request itself lives in
// o.guardApprovals, and the input-required entry that shows it to a human
// is created by the event loop.
func (o *Orchestrator) RequestGuardrailApproval(identifier, runID string, req guardrails.ApprovalRequest) (guardrails.ApprovalStatus, error) {
	if o.guardApprovals == nil {
		return guardrails.ApprovalStatus{}, errors.New("orchestrator: guardrail approvals are not configured")
	}
	req.Command = logging.RedactString(req.Command)
	if req.Lane != "" && req.Lane != guardrails.LaneHuman {
		return guardrails.ApprovalStatus{}, fmt.Errorf("orchestrator: only a human-lane push asks for approval, not %q", req.Lane)
	}
	req.Paths, req.PathsTotal = sanitizeLanePaths(req.Paths, req.PathsTotal)
	st, created := o.guardApprovals.Request(identifier, runID, req)
	if !created {
		return st, nil
	}
	select {
	case o.events <- OrchestratorEvent{Type: EventGuardrailApproval, Identifier: identifier,
		guardrail: &guardrailApprovalEvent{ID: st.ID, RunID: runID, Command: req.Command,
			Lane: req.Lane, Paths: req.Paths, PathsTotal: req.PathsTotal}}:
		return st, nil
	default:
		o.guardApprovals.Remove(st.ID)
		metrics.EventDropped()
		return guardrails.ApprovalStatus{}, ErrBusy
	}
}

// GuardrailApprovalStatus is a run's view of its approval request; an
// approved request is spent by the read that sees it. Safe from any
// goroutine.
func (o *Orchestrator) GuardrailApprovalStatus(identifier, runID, id string) guardrails.ApprovalStatus {
	if o.guardApprovals == nil {
		return guardrails.ApprovalStatus{ID: id, Status: guardrails.StatusUnknown}
	}
	return o.guardApprovals.Status(identifier, runID, id)
}

// ProvideAgentInput is ProvideInput from an agent (the token-gated
// agent-actions route): it answers an ordinary input request, but never a
// guardrail approval. Safe from any goroutine.
func (o *Orchestrator) ProvideAgentInput(identifier, message string) error {
	if o.isDraining() {
		return ErrDraining
	}
	select {
	case o.events <- OrchestratorEvent{Type: EventProvideInput, Identifier: identifier, Message: message, FromAgent: true}:
		return nil
	default:
		metrics.EventDropped()
		slog.Warn("orchestrator: provide-input event channel full", "identifier", identifier)
		return ErrBusy
	}
}

// guardrailQuestion is the text a human sees for an approval request.
func guardrailQuestion(g *guardrailApprovalEvent) string {
	if g.Lane == guardrails.LaneHuman {
		return "Pipeline change: the agent wants to push changes to " +
			guardrails.LanePathSummary(g.Paths, g.PathsTotal, guardrails.MaxLanePathsShown) +
			" (`" + g.Command + "`)\n\nThese files are in guardrails.lanes.human: an agent may change what CI checks, never how CI checks. " +
			"Review the change, then reply `approve` to let this push through once; any other reply rejects it and is passed to the agent as the reason. " +
			"Itervox will not auto-merge this pull request."
	}
	command := g.Command
	return "Agent wants to run: `" + command + "`\n\nThis command is in guardrails.approval_commands. " +
		"Reply `approve` to run it once; any other reply rejects it and is passed to the agent as the reason."
}

// handleGuardrailApproval shows an approval request to a human as an
// input-required entry of the running issue. Event loop.
func (o *Orchestrator) handleGuardrailApproval(state *State, ev OrchestratorEvent) {
	g := ev.guardrail
	if g == nil || o.guardApprovals == nil {
		return
	}
	var live *RunEntry
	for _, r := range state.Running {
		if r != nil && r.Issue.Identifier == ev.Identifier && r.SessionID == g.RunID {
			live = r
			break
		}
	}
	if live == nil {
		o.guardApprovals.Decide(g.ID, false, "the run that asked is no longer running")
		return
	}
	if existing := state.InputRequiredIssues[ev.Identifier]; existing != nil {
		o.guardApprovals.Decide(g.ID, false, "another request is already waiting for the operator; ask again after it is answered")
		return
	}
	entry := &InputRequiredEntry{
		IssueID:             live.Issue.ID,
		Identifier:          ev.Identifier,
		Context:             guardrailQuestion(g),
		Backend:             live.Backend,
		WorkerHost:          live.WorkerHost,
		ProfileName:         live.ProfileName,
		QueuedAt:            time.Now(),
		GuardrailApprovalID: g.ID,
	}
	if g.Lane == guardrails.LaneHuman {
		entry.GuardrailLanePaths, entry.GuardrailLanePathsTotal = slices.Clone(g.Paths), g.PathsTotal
	}
	entry.QuestionCommentKey = o.newInputRequiredCommentKey(ev.Identifier)
	body := tracker.MarkManagedComment(buildInputRequiredComment(entry, o.InlineInputCfg()))
	if err := o.postInputRequiredComment(entry.IssueID, ev.Identifier, entry.QuestionCommentKey, body, "question"); err != nil {
		o.postQuestionKeyless(entry.IssueID, ev.Identifier, entry.QuestionCommentKey, body, err)
	}
	state.InputRequiredIssues[ev.Identifier] = entry
	p := feedRunPayload(live) // #213: the command stays out of the feed
	p.Kind = "guardrail_approval"
	o.emitFeed(state, FeedInputRequired, ev.Identifier, p)
	slog.Info("guardrails: approval requested", "issue_identifier", ev.Identifier, "command", g.Command)
	if o.logBuf != nil {
		o.logBuf.Add(ev.Identifier, makeBufLineWithSession("INFO", "guardrails: waiting for approval: "+g.Command, g.RunID))
	}
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// resolveGuardrailApproval answers entry's approval with a human reply
// (via "dashboard" — also the Mac app —, "slack" (#230), "tracker", or
// "dismiss"): the request is decided and the entry removed; nothing is
// resumed — the run never stopped. A dashboard or Slack answer is mirrored
// to the tracker like any input-required reply. Event loop.
func (o *Orchestrator) resolveGuardrailApproval(state *State, entry *InputRequiredEntry, reply, via string) {
	approved := guardrails.IsApproval(reply)
	command, runID := "", ""
	lane := entry.GuardrailLanePaths != nil
	var laneReport guardrails.LaneReport
	if a, ok := o.guardApprovals.Get(entry.GuardrailApprovalID); ok {
		command, runID = a.Command, a.RunID // read first: the shim may spend the decision at once
		lane = lane || a.Lane == guardrails.LaneHuman
		laneReport = guardrails.LaneReport{Lane: guardrails.LaneHuman, Paths: a.Paths, PathsTotal: a.PathsTotal}
	}
	rejectedKind := guardrails.KindApprovalRejected
	if lane {
		rejectedKind = guardrails.KindLaneRejected
		if approved { // the push goes through: its pull request is a human's to merge
			backend := ""
			for _, r := range state.Running {
				if r != nil && r.Issue.Identifier == entry.Identifier && r.SessionID == runID {
					backend = laneBackend(r.Backend)
				}
			}
			recordPushLane(state, entry.Identifier, runID, backend, laneReport, time.Now())
			o.savePushLanesToDisk(clonePushLanes(state.PushLanes))
		}
	}
	o.guardApprovals.Decide(entry.GuardrailApprovalID, approved, reply)
	delete(state.InputRequiredIssues, entry.Identifier)
	restartStallClock(state, entry.Identifier, time.Now())
	if approved {
		o.emitFeed(state, FeedGuardrailApproved, entry.Identifier, FeedPayload{Via: via}) // #213
		slog.Info("guardrails: approved", "issue_identifier", entry.Identifier, "via", via, "command", command)
		if o.logBuf != nil {
			o.logBuf.Add(entry.Identifier, makeBufLine("INFO", "guardrails: approved via "+via+": "+command))
		}
	} else {
		slog.Warn("guardrails: refused", "issue_identifier", entry.Identifier, "kind", rejectedKind, "via", via, "command", command)
		metrics.GuardrailBlock(rejectedKind)
		o.emitFeed(state, FeedGuardrailBlocked, entry.Identifier, FeedPayload{Kind: rejectedKind, Via: via}) // #213
		appendRecentFailure(state, FailureRecord{Kind: FailureKindGuardrail, Identifier: entry.Identifier,
			Source: rejectedKind, Message: "rejected via " + via + ": " + command, OccurredAt: time.Now()}, time.Now())
		if o.logBuf != nil {
			o.logBuf.Add(entry.Identifier, makeBufLine("WARN", "guardrails: rejected via "+via+": "+command))
		}
	}
	if via == "dashboard" || via == InputViaSlack {
		// Mirror a human's answer like any input-required reply.
		if err := o.postInputRequiredComment(entry.IssueID, entry.Identifier,
			o.newInputRequiredCommentKey(entry.Identifier), tracker.MarkManagedComment(reply), "reply"); err != nil {
			slog.Warn("guardrails: approval answer not mirrored to the tracker", "issue_identifier", entry.Identifier, "error", err)
		}
	}
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// dropGuardrailApprovals ends identifier's approval requests when its run
// exits: the waiting entry goes, and a shim still polling is refused.
// Event loop.
func (o *Orchestrator) dropGuardrailApprovals(state *State, identifier string) {
	if e := state.InputRequiredIssues[identifier]; e != nil && e.GuardrailApprovalID != "" {
		delete(state.InputRequiredIssues, identifier)
	}
	if o.guardApprovals != nil {
		o.guardApprovals.Drop(identifier)
	}
}

// awaitingGuardrailApproval reports whether run is blocked waiting for an
// operator's answer to its guardrail approval: the issue's input-required
// entry is an approval request (one is only raised for the live run, and
// dropGuardrailApprovals removes it when the run exits). Event loop.
func awaitingGuardrailApproval(state State, run *RunEntry) bool {
	if run == nil {
		return false
	}
	e := state.InputRequiredIssues[run.Issue.Identifier]
	return e != nil && e.GuardrailApprovalID != ""
}

// restartStallClock restarts the stall clock of identifier's running
// entries once their approval is answered: the wait was not inactivity
// (ReconcileStalls skips a run while it waits), so the agent gets a full
// agent.stall_timeout_ms from the answer. Event loop.
func restartStallClock(state *State, identifier string, now time.Time) {
	for _, r := range state.Running {
		if r != nil && r.Issue.Identifier == identifier {
			t := now
			r.LastEventAt = &t
		}
	}
}
