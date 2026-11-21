package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/tracker"
)

// Guardrails (#204). A dispatched local run gets a private runtime
// directory (internal/guardrails): PATH shims for git, gh, glab and the
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
	Lane         string
	Paths        []string
	PathsTotal   int
	Reasons      []guardrails.LaneReason
	ReasonsTotal int
	// ReasonsUnavailable: the reasons could not be recomputed.
	ReasonsUnavailable bool
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

// prepareRunGuardrails prepares a local run's guardrails and returns the
// environment the runner applies to the agent (agent.WithRunEnv), the
// runtime and its token (nil/"" when the guardrails are off, the run is
// remote, or preparation failed — the run then proceeds unguarded with a
// warning). prBase is the branch the issue's pull request targets: pushes
// are classified into guardrails.lanes against it. patchOut is push:
// daemon: the agent's environment then carries no forge credentials and its
// pushes are refused (a patch-out run whose guardrails cannot be prepared
// must not start; the worker checks). Worker goroutine; touches no State.
func (o *Orchestrator) prepareRunGuardrails(ctx context.Context, identifier, runID, wsPath, workerHost, branch, prBase string, turnTimeoutMs, maxTurns int, patchOut bool) (agent.RunEnv, *guardrails.Runtime, string) {
	g := o.cfg.Guardrails
	if !g.Enabled {
		return agent.RunEnv{}, nil, ""
	}
	if workerHost != "" {
		slog.Warn("guardrails: not applied to a run on an SSH worker host (v1 guards local runs only)",
			"issue_identifier", identifier, "worker_host", workerHost)
		return agent.RunEnv{}, nil, ""
	}
	exe, err := o.guardrailExecutable()
	if err != nil {
		slog.Warn("guardrails: cannot resolve the itervox binary; run is unguarded", "issue_identifier", identifier, "error", err)
		return agent.RunEnv{}, nil, ""
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
	rt, err := guardrails.Prepare(ctx, opts)
	if err != nil {
		o.agentActionTokens.Revoke(token)
		slog.Warn("guardrails: cannot prepare; run is unguarded", "issue_identifier", identifier, "error", err)
		return agent.RunEnv{}, nil, ""
	}
	return rt.AgentEnv(), rt, token
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

// RequestGuardrailApproval raises (or joins) an approval request for an
// agent's command or held push (the token-gated guardrail route). Safe
// from any goroutine: the request itself lives in o.guardApprovals, and
// the input-required entry that shows it to a human is created by the
// event loop. A held push's reasons are recomputed here, from the issue's
// worktree and the refs the hook named (agentPushReasons: git on the
// caller's goroutine, an HTTP handler's); whatever reasons the request
// carries are discarded, since the hook runs in the agent's environment.
func (o *Orchestrator) RequestGuardrailApproval(identifier, runID string, req guardrails.ApprovalRequest) (guardrails.ApprovalStatus, error) {
	req.Reasons, req.ReasonsTotal, req.ReasonsUnavailable = nil, 0, false
	if req.Lane == guardrails.LaneHuman && o.guardApprovals != nil {
		req.Reasons, req.ReasonsTotal, req.ReasonsUnavailable = o.agentPushReasons(identifier, req.Refs)
	}
	return o.raiseGuardrailApproval(identifier, runID, req)
}

// agentPushReasons recomputes why an agent's held push needs a person:
// the daemon's own lanes and base refs, the issue's worktree, the pushed
// refs. unavailable when they cannot be recomputed (the question then
// shows the paths only, with a note). Caller's goroutine, never the loop.
func (o *Orchestrator) agentPushReasons(identifier string, refs []guardrails.PushedRef) (reasons []guardrails.LaneReason, total int, unavailable bool) {
	dir, err := o.worktreePath(identifier)
	if err != nil || o.cfg == nil {
		return nil, 0, true
	}
	bases, err := o.diffBases(identifier, "")
	if err != nil {
		return nil, 0, true
	}
	g := o.cfg.Guardrails // read-only after startup (not in the cfgMu list)
	reasons, total, err = guardrails.AgentPushReasons(dir, refs,
		guardrails.Lanes{Human: g.Lanes.Human, SecondOpinion: g.Lanes.SecondOpinion}, bases)
	if err != nil {
		slog.Warn("guardrails: cannot recompute a held push's reasons", "issue_identifier", identifier, "error", err)
		return nil, 0, true
	}
	return reasons, total, false
}

// raiseGuardrailApproval raises (or joins) an approval request whose
// reasons, if any, the daemon computed itself (RequestGuardrailApproval
// for an agent, awaitDaemonPushApproval for a daemon push).
func (o *Orchestrator) raiseGuardrailApproval(identifier, runID string, req guardrails.ApprovalRequest) (guardrails.ApprovalStatus, error) {
	if o.guardApprovals == nil {
		return guardrails.ApprovalStatus{}, errors.New("orchestrator: guardrail approvals are not configured")
	}
	req.Command = logging.RedactString(req.Command)
	if req.Lane != "" && req.Lane != guardrails.LaneHuman {
		return guardrails.ApprovalStatus{}, fmt.Errorf("orchestrator: only a human-lane push asks for approval, not %q", req.Lane)
	}
	req.Paths, req.PathsTotal = sanitizeLanePaths(req.Paths, req.PathsTotal)
	req.Reasons, req.ReasonsTotal = guardrails.SanitizeLaneReasons(req.Reasons, req.ReasonsTotal)
	if req.Lane != guardrails.LaneHuman {
		req.Reasons, req.ReasonsTotal, req.ReasonsUnavailable = nil, 0, false
	}
	st, created := o.guardApprovals.Request(identifier, runID, req)
	if !created {
		return st, nil
	}
	select {
	case o.events <- OrchestratorEvent{Type: EventGuardrailApproval, Identifier: identifier,
		guardrail: &guardrailApprovalEvent{ID: st.ID, RunID: runID, Command: req.Command,
			Lane: req.Lane, Paths: req.Paths, PathsTotal: req.PathsTotal,
			Reasons: req.Reasons, ReasonsTotal: req.ReasonsTotal, ReasonsUnavailable: req.ReasonsUnavailable}}:
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
			" (`" + g.Command + "`)\n\n" + laneReasonsBlock(g.Reasons, g.ReasonsTotal, g.ReasonsUnavailable) +
			"These files are in guardrails.lanes.human: an agent may change what CI checks, never how CI checks. " +
			"Review the change, then reply `approve` to let this push through once; any other reply rejects it and is passed to the agent as the reason. " +
			"Itervox will not auto-merge this pull request."
	}
	command := g.Command
	return "Agent wants to run: `" + command + "`\n\nThis command is in guardrails.approval_commands. " +
		"Reply `approve` to run it once; any other reply rejects it and is passed to the agent as the reason."
}

// laneReasonsUnavailableNote is the "why" of a held push whose reasons the
// daemon could not recompute.
const laneReasonsUnavailableNote = "reasons unavailable (Itervox could not recompute them from the worktree; the files are listed above)"

// laneReasonsBlock is the "why" list of a held push's question (it is the
// tracker comment, and the text Slack and viewers see): one Markdown
// bullet per reason (at most guardrails.MaxLaneReasonsShown) with the
// location, the rule's message and id, never the added line itself; a
// note when the reasons are unavailable; "" when there is none.
func laneReasonsBlock(reasons []guardrails.LaneReason, total int, unavailable bool) string {
	if unavailable {
		return "Why this needs a person: " + laneReasonsUnavailableNote + "\n\n"
	}
	if len(reasons) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Why this needs a person:\n")
	shown := reasons[:min(len(reasons), guardrails.MaxLaneReasonsShown)]
	for _, r := range shown {
		b.WriteString("- " + r.Markdown() + "\n")
	}
	if more := max(total, len(reasons)) - len(shown); more > 0 {
		b.WriteString("- and " + strconv.Itoa(more) + " more\n")
	}
	b.WriteString("\n")
	return b.String()
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
		entry.GuardrailLaneReasons, entry.GuardrailLaneReasonsTotal = slices.Clone(g.Reasons), g.ReasonsTotal
		entry.GuardrailLaneReasonsUnavailable = g.ReasonsUnavailable
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
