package server

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/logging"
)

// evidence.go — the per-issue evidence report: what ran, what was checked,
// who approved.
//
//	GET /api/v1/issues/{identifier}/evidence   viewer   the issue's report
//
// The report is assembled off the event loop (cmd/itervox, from Snapshot(),
// the run history, the event feed and the attestation store) from data the
// daemon already keeps; this file only defines its shape and redacts it for
// the caller's scope. It carries hashes, ids, names, counts and outcomes:
// never prompt text, agent output, a reviewer's findings, a pushed file's
// lines or a command line. Below operator the steering message text and
// who answered an approval are left out.
//
// EvidenceClient is the seam: the daemon's implementation reads what this
// process keeps (bounded run history, the 7-day event feed); another
// implementation can serve the same shape from a longer-lived store.

// CapabilityIssueEvidence is advertised in StateSnapshot.Capabilities when
// the daemon serves GET /api/v1/issues/{identifier}/evidence.
const CapabilityIssueEvidence = "issue_evidence"

// EvidenceReport is GET /api/v1/issues/{identifier}/evidence.
type EvidenceReport struct {
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title,omitempty"`
	GeneratedAt time.Time `json:"generatedAt"`
	// Scope is the caller's scope the report was redacted for.
	Scope string `json:"scope"`
	// Runs are the issue's runs, oldest first; a live run is last.
	Runs []EvidenceRun `json:"runs"`
	// Retry is the issue's scheduled retry, when one is due.
	Retry *EvidenceRetry `json:"retry,omitempty"`
	// Reviews are the reviewer verdicts of this daemon session and the
	// quorum outcome once the chain closed.
	Reviews EvidenceReviews `json:"reviews"`
	// SignedChecks are the issue's signed check records, summarized per
	// run; GET .../attestations has every record.
	SignedChecks []EvidenceSignedRun `json:"signedChecks"`
	// GuardrailBlocks are the guardrail refusals of the issue's agents
	// (kind only: never the command).
	GuardrailBlocks []EvidenceGuardrailBlock `json:"guardrailBlocks"`
	// PushLane is the highest guardrails.lanes lane the issue's pushes
	// reached (second_opinion or human); absent for auto.
	PushLane *EvidencePushLane `json:"pushLane,omitempty"`
	// Approvals are the human decisions: guardrail approvals (a held
	// command or a held push) and answers to the agent's questions,
	// oldest first, a pending request last.
	Approvals []EvidenceApproval `json:"approvals"`
	// PullRequests are the PRs the issue's runs recorded.
	PullRequests []EvidencePullRequest `json:"pullRequests"`
	// Limits say how far back the sources reach.
	Limits EvidenceLimits `json:"limits"`
}

// EvidenceRun is one run of the issue.
type EvidenceRun struct {
	RunID string `json:"runId,omitempty"`
	// Kind is "worker", "reviewer" or "automation".
	Kind    string `json:"kind"`
	Profile string `json:"profile,omitempty"`
	Backend string `json:"backend,omitempty"`
	// ProfileModel is the model the profile's command names now (--model):
	// the run itself does not record its model.
	ProfileModel string `json:"profileModel,omitempty"`
	WorkerHost   string `json:"workerHost,omitempty"`
	// Status is the run's outcome ("succeeded", "failed", "cancelled",
	// "stalled", "input_required"), or "running" for the live run.
	Status     string     `json:"status"`
	Live       bool       `json:"live,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	ElapsedMs  int64      `json:"elapsedMs"`
	TurnCount  int        `json:"turnCount"`
	// RetryAttempt is the live run's retry attempt (absent on a first run).
	RetryAttempt int    `json:"retryAttempt,omitempty"`
	AutomationID string `json:"automationId,omitempty"`
	TriggerType  string `json:"triggerType,omitempty"`
	// PromptDigest / PromptParts identify the prompt version (#186):
	// hashes only.
	PromptDigest string              `json:"promptDigest,omitempty"`
	PromptParts  *HistoryPromptParts `json:"promptParts,omitempty"`
	RulesHash    string              `json:"rulesHash,omitempty"`
	RuleIDs      []string            `json:"ruleIds,omitempty"`
	MemoryIDs    []string            `json:"memoryIds,omitempty"`
	Playbook     string              `json:"playbook,omitempty"`
	// Steers are the operator messages delivered to the run (redacted when
	// recorded); below operator only their time and delivery.
	Steers        []EvidenceSteer `json:"steers,omitempty"`
	SteersDropped int             `json:"steersDropped,omitempty"`
	BranchName    string          `json:"branchName,omitempty"`
	PRURL         string          `json:"prUrl,omitempty"`
	// ReviewVerdict is a reviewer run's verdict ("approve" or "block").
	ReviewVerdict string `json:"reviewVerdict,omitempty"`
	// Checks are the run's gate outcomes; absent when no gate ran (or the
	// run predates their recording).
	Checks *EvidenceRunChecks `json:"checks,omitempty"`
}

// EvidenceSteer is one operator message delivered to a run.
type EvidenceSteer struct {
	At time.Time `json:"at"`
	// Delivery is "live" or "resume".
	Delivery string `json:"delivery"`
	// Text is the message as recorded (redacted); operator and admin only.
	Text string `json:"text,omitempty"`
}

// EvidenceRunChecks are a run's gates: the #80 evidence gate
// (require_evidence) and the signed checks (verify).
type EvidenceRunChecks struct {
	Evidence *EvidenceGate `json:"evidence,omitempty"`
	Signed   *EvidenceGate `json:"signed,omitempty"`
}

// EvidenceGate is one gate's outcome: check names and the decision.
type EvidenceGate struct {
	Required  []string  `json:"required"`
	Failed    []string  `json:"failed,omitempty"`
	Passed    bool      `json:"passed"`
	CheckedAt time.Time `json:"checkedAt"`
	// AttestRunID is the signed-check run id (SignedChecks[].attestRunId).
	AttestRunID string `json:"attestRunId,omitempty"`
}

// EvidenceRetry is the issue's scheduled retry.
type EvidenceRetry struct {
	Attempt int       `json:"attempt"`
	DueAt   time.Time `json:"dueAt"`
}

// EvidenceReviews are the issue's reviewer verdicts.
type EvidenceReviews struct {
	Verdicts []EvidenceVerdict `json:"verdicts"`
	Outcome  *EvidenceOutcome  `json:"outcome,omitempty"`
}

// EvidenceVerdict is one reviewer's verdict: the decision and how many
// reasons and line comments it gave, never their text.
type EvidenceVerdict struct {
	Profile    string    `json:"profile"`
	Verdict    string    `json:"verdict"`
	Backend    string    `json:"backend,omitempty"`
	RecordedAt time.Time `json:"recordedAt"`
	Reasons    int       `json:"reasons"`
	Comments   int       `json:"comments"`
}

// EvidenceOutcome is the review quorum's decision.
type EvidenceOutcome struct {
	Blocked   bool   `json:"blocked"`
	Approvals int    `json:"approvals"`
	Blocks    int    `json:"blocks"`
	Quorum    string `json:"quorum,omitempty"`
}

// EvidenceSignedRun summarizes one run's signed check records.
type EvidenceSignedRun struct {
	AttestRunID string                `json:"attestRunId"`
	Profile     string                `json:"profile,omitempty"`
	Checks      []EvidenceSignedCheck `json:"checks"`
	// Sealed: the run's records end with a seal. Verified: every record's
	// signature is valid for the daemon's key and on the sealed tree.
	Sealed   bool `json:"sealed"`
	Verified bool `json:"verified"`
}

// EvidenceSignedCheck is one signed check: never its command or output.
type EvidenceSignedCheck struct {
	Name       string    `json:"name"`
	Passed     bool      `json:"passed"`
	ExitCode   int       `json:"exitCode"`
	TimedOut   bool      `json:"timedOut,omitempty"`
	Verified   bool      `json:"verified"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMs int64     `json:"durationMs"`
}

// EvidenceGuardrailBlock is one guardrail refusal.
type EvidenceGuardrailBlock struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
}

// EvidencePushLane is the issue's PushLanes record: the lane and how many
// files reached it, never the paths.
type EvidencePushLane struct {
	Lane           string    `json:"lane"`
	PathsTotal     int       `json:"pathsTotal"`
	PathsKept      int       `json:"pathsKept"`
	Truncated      bool      `json:"truncated"`
	WriterBackends []string  `json:"writerBackends,omitempty"`
	RunID          string    `json:"runId,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// Approval kinds and decisions.
const (
	EvidenceApprovalGuardrail = "guardrail" // a held command or push (#204)
	EvidenceApprovalInput     = "input"     // an answer to the agent's question

	EvidenceDecisionPending   = "pending"
	EvidenceDecisionApproved  = "approved"
	EvidenceDecisionRejected  = "rejected"
	EvidenceDecisionAnswered  = "answered"
	EvidenceDecisionDismissed = "dismissed"
)

// EvidenceApproval is one human decision (or a request still waiting).
type EvidenceApproval struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Decision string    `json:"decision"`
	// Via is the channel: "dashboard", "slack", "tracker", "dismiss",
	// "agent" (an agent's relayed answer; never for a guardrail approval).
	Via   string `json:"via,omitempty"`
	RunID string `json:"runId,omitempty"`
	// Push marks a held push (guardrails.lanes human lane); PathsTotal is
	// how many files held it (a pending request only).
	Push       bool `json:"push,omitempty"`
	PathsTotal int  `json:"pathsTotal,omitempty"`
	// Actor names who decided ("dashboard:<token id>",
	// "dashboard:bootstrap", "tracker:<name>"); operator and admin only.
	// ActorName is the stored token's name; admin only.
	Actor     string `json:"actor,omitempty"`
	ActorName string `json:"actorName,omitempty"`
}

// EvidencePullRequest is one PR the issue's runs recorded.
type EvidencePullRequest struct {
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
	// State is "merged" when this daemon saw the merge, else "recorded"
	// (open, or merged elsewhere).
	State      string     `json:"state"`
	RecordedAt *time.Time `json:"recordedAt,omitempty"`
	MergedAt   *time.Time `json:"mergedAt,omitempty"`
}

// EvidenceLimits say how far back the report's sources reach.
type EvidenceLimits struct {
	// FeedSince is the oldest event the feed still holds (approvals,
	// guardrail blocks, merges come from it); absent when it is empty.
	FeedSince       *time.Time `json:"feedSince,omitempty"`
	FeedMaxAgeHours int        `json:"feedMaxAgeHours"`
	// HistoryRuns is how many of the issue's runs the run history holds.
	HistoryRuns int `json:"historyRuns"`
	// ReviewsSessionOnly: reviewer verdicts are kept for this daemon
	// session only.
	ReviewsSessionOnly bool `json:"reviewsSessionOnly"`
}

// EvidenceClient assembles an issue's report, unredacted (the handler
// redacts it for the caller). Safe from any goroutine; never on the event
// loop.
type EvidenceClient interface {
	IssueEvidence(identifier string) (EvidenceReport, error)
}

func (s *Server) handleIssueEvidence(w http.ResponseWriter, r *http.Request) {
	if s.evidence == nil {
		writeError(w, http.StatusServiceUnavailable, "not_supported", "the evidence report is not available")
		return
	}
	id := chi.URLParam(r, "identifier")
	rep, err := s.evidence.IssueEvidence(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "evidence_unreadable", "the evidence report could not be assembled: "+logging.RedactString(err.Error()))
		return
	}
	rep.Identifier = id
	p, _ := principalFrom(r.Context())
	writeJSON(w, http.StatusOK, evidenceForScope(rep, p.Scope, s.tokenNames(p.Scope)))
}

// tokenNames maps stored token ids to their names for an admin (the
// Access page shows them to admins only); nil otherwise.
func (s *Server) tokenNames(scope apitokens.Scope) map[string]string {
	if scope != apitokens.ScopeAdmin || s.tokens == nil {
		return nil
	}
	list, err := s.tokens.List()
	if err != nil {
		return nil
	}
	names := make(map[string]string, len(list))
	for _, t := range list {
		names[t.ID] = t.Name
	}
	return names
}

// evidenceForScope returns rep as a caller of scope may see it. Below
// operator: no steering text, no approver. Operator: the approver's actor
// id. Admin: also the stored token's name. Every list is non-nil. rep's
// slices are not shared with other callers, but they are copied anyway so
// the function is safe on a shared report.
func evidenceForScope(rep EvidenceReport, scope apitokens.Scope, tokenNames map[string]string) EvidenceReport {
	if scope == "" {
		scope = apitokens.ScopeViewer
	}
	rep.Scope = string(scope)
	operator := scope.Allows(apitokens.ScopeOperator)
	runs := make([]EvidenceRun, len(rep.Runs))
	for i, run := range rep.Runs {
		if len(run.Steers) > 0 {
			steers := slices.Clone(run.Steers)
			for j := range steers {
				if operator {
					steers[j].Text = logging.RedactString(steers[j].Text)
				} else {
					steers[j].Text = ""
				}
			}
			run.Steers = steers
		}
		runs[i] = run
	}
	rep.Runs = runs
	approvals := make([]EvidenceApproval, len(rep.Approvals))
	for i, a := range rep.Approvals {
		a.ActorName = ""
		switch {
		case !operator:
			a.Actor = ""
		case scope == apitokens.ScopeAdmin:
			if id, ok := strings.CutPrefix(a.Actor, "dashboard:"); ok {
				a.ActorName = tokenNames[id]
			}
		}
		approvals[i] = a
	}
	rep.Approvals = approvals
	if rep.SignedChecks == nil {
		rep.SignedChecks = []EvidenceSignedRun{}
	}
	if rep.GuardrailBlocks == nil {
		rep.GuardrailBlocks = []EvidenceGuardrailBlock{}
	}
	if rep.PullRequests == nil {
		rep.PullRequests = []EvidencePullRequest{}
	}
	if rep.Reviews.Verdicts == nil {
		rep.Reviews.Verdicts = []EvidenceVerdict{}
	}
	return rep
}
