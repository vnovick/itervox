package evals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
)

// LiveOptInEnv must be "1" for RunLive to start any agent: live mode spends
// real tokens, so `make verify` and a plain `go test` can never reach it.
const LiveOptInEnv = "ITERVOX_EVALS_LIVE"

// ErrLiveNotEnabled is returned by RunLive without the explicit opt-in.
var ErrLiveNotEnabled = errors.New("evals: live mode runs a real agent and spends tokens; set " + LiveOptInEnv + "=1 to run it")

// CandidateName is the file a re-recording writes next to a fixture. It is
// never read by recorded mode; PromoteCandidate turns it into
// recording.jsonl after a human reviewed it.
const CandidateName = "recording.candidate.jsonl"

// Budget bounds a live run. Zero fields take the defaults below.
type Budget struct {
	// MaxTurns caps the agent's own turn loop (claude's --max-turns). The
	// codex CLI has no equivalent flag; MaxTokens and Timeout bound it.
	MaxTurns int
	// MaxTokens caps one scenario's input+cached+output tokens. The run is
	// cancelled as soon as a progress update crosses it.
	MaxTokens int
	// MaxCostUSD caps the whole suite's reported cost. Once reached, the
	// remaining scenarios are not started and fail with a budget error.
	MaxCostUSD float64
	// Timeout is one scenario's wall-clock limit.
	Timeout time.Duration
}

// Budget defaults: small enough that a mistake costs cents, large enough for
// the read → decide → act loop the merge-bot and reviewer profiles run.
const (
	DefaultMaxTurns   = 12
	DefaultMaxTokens  = 400_000
	DefaultMaxCostUSD = 3.0
	DefaultTimeout    = 5 * time.Minute
)

func (b Budget) withDefaults() Budget {
	if b.MaxTurns <= 0 {
		b.MaxTurns = DefaultMaxTurns
	}
	if b.MaxTokens <= 0 {
		b.MaxTokens = DefaultMaxTokens
	}
	if b.MaxCostUSD <= 0 {
		b.MaxCostUSD = DefaultMaxCostUSD
	}
	if b.Timeout <= 0 {
		b.Timeout = DefaultTimeout
	}
	return b
}

// LiveOptions configures RunLive.
type LiveOptions struct {
	// FixturesRoot holds <profile>/<scenario>/ fixture directories.
	FixturesRoot string
	// Workflow is the WORKFLOW.md whose profiles (SOUL.md, INSTRUCTIONS.md,
	// allowed actions, command), prompt template, automations and rules
	// layers the run uses.
	Workflow string
	// Profile and Scenario filter the suite ("" = all).
	Profile  string
	Scenario string
	// Command overrides the profile's agent command (e.g. a pinned CLI
	// path); Backend overrides the backend ("claude" | "codex").
	Command string
	Backend string
	Budget  Budget
	// Candidate writes each run next to its fixture as CandidateName. When
	// false, runs are written under OutDir/<profile>/<scenario>/recording.jsonl.
	// Neither mode ever writes a fixture's recording.jsonl.
	Candidate bool
	OutDir    string
	// Runner executes the agent turn; nil uses the daemon's runners
	// (claude, with codex selected by backend).
	Runner agent.Runner
	// Log receives progress lines; nil discards them.
	Log io.Writer
}

// LiveRun is one scenario's live outcome.
type LiveRun struct {
	Profile, Scenario string
	RecordingPath     string // "" when nothing was written
	Tokens            int
	CostUSD           float64
}

// LiveResult is RunLive's outcome: the judged report plus per-run detail.
type LiveResult struct {
	Report Report
	Runs   []LiveRun
}

// RunLive runs every selected scenario against a real backend CLI, records
// the transcript, and judges it with the recorded-mode judges.
func RunLive(ctx context.Context, opts LiveOptions) (LiveResult, error) {
	if os.Getenv(LiveOptInEnv) != "1" {
		return LiveResult{}, ErrLiveNotEnabled
	}
	if !opts.Candidate && opts.OutDir == "" {
		return LiveResult{}, errors.New("evals: live mode needs an output directory (or candidate mode)")
	}
	cfg, err := config.Load(opts.Workflow)
	if err != nil {
		return LiveResult{}, fmt.Errorf("evals: load %s: %w", opts.Workflow, err)
	}
	scenarios, err := LoadScenarios(opts.FixturesRoot, opts.Profile)
	if err != nil {
		return LiveResult{}, err
	}
	if opts.Scenario != "" {
		scenarios = slices.DeleteFunc(scenarios, func(s Scenario) bool { return s.Name != opts.Scenario })
	}
	if len(scenarios) == 0 {
		return LiveResult{}, fmt.Errorf("evals: no scenarios match profile=%q scenario=%q under %s", opts.Profile, opts.Scenario, opts.FixturesRoot)
	}
	if opts.Runner == nil {
		opts.Runner = agent.NewMultiRunner(agent.NewClaudeRunner(), map[string]agent.Runner{"codex": agent.NewCodexRunner()})
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	opts.Budget = opts.Budget.withDefaults()

	var res LiveResult
	spent := 0.0
	for _, sc := range scenarios {
		sc = withExpectation(sc)
		if spent >= opts.Budget.MaxCostUSD {
			v := ScenarioVerdict{Profile: sc.Profile, Scenario: sc.Name,
				Error: fmt.Sprintf("budget: suite cost cap $%.2f reached ($%.4f spent); not run", opts.Budget.MaxCostUSD, spent)}
			res.Report.Verdicts = append(res.Report.Verdicts, v)
			continue
		}
		_, _ = fmt.Fprintf(opts.Log, "[evals] live %s/%s ...\n", sc.Profile, sc.Name)
		v, run := runLiveScenario(ctx, cfg, sc, opts)
		spent += run.CostUSD
		res.Report.Verdicts = append(res.Report.Verdicts, v)
		res.Runs = append(res.Runs, run)
		_, _ = fmt.Fprintf(opts.Log, "[evals]   tokens=%d cost=$%.4f recording=%s\n", run.Tokens, run.CostUSD, run.RecordingPath)
	}
	return res, nil
}

// withExpectation loads expected.yaml into the scenario when it has not
// been loaded yet.
func withExpectation(s Scenario) Scenario {
	e := s.Expect
	if e.RequiredActionCalls == nil && e.MarkerPhrases == nil && e.ForbiddenActions == nil && e.ForbiddenPhrases == nil {
		_ = readExpectedYAML(s.Dir, &s.Expect)
	}
	return s
}

// automationFor returns the dispatch the scenario stands in for: the first
// enabled automation in the eval workflow bound to the scenario's profile,
// with the input's comment as its trigger comment. nil when the profile has
// no automation (a plain profile dispatch).
func automationFor(cfg *config.Config, profile string, in ScenarioInput, now time.Time) *orchestrator.AutomationDispatch {
	for _, a := range cfg.Automations {
		if !a.Enabled || a.Profile != profile {
			continue
		}
		d := &orchestrator.AutomationDispatch{
			AutomationID: a.ID,
			ProfileName:  profile,
			Instructions: a.Instructions,
			Trigger: orchestrator.AutomationTriggerContext{
				Type: a.Trigger.Type, FiredAt: now, AutomationID: a.ID, CurrentState: in.IssueState,
			},
		}
		if a.Trigger.Type == config.AutomationTriggerTrackerComment {
			d.Trigger.CommentID = "eval-comment-1"
			d.Trigger.CommentBody = in.CommentBody
			d.Trigger.CommentAuthorID = "eval-operator"
			d.Trigger.CommentAuthorName = in.commentAuthor()
		}
		return d
	}
	return nil
}

// resolveCommand picks the command and backend the scenario runs with:
// the overrides, else the profile's own command and backend.
func resolveCommand(opts LiveOptions, profile config.AgentProfile) (command, backend string) {
	command = strings.TrimSpace(opts.Command)
	if command == "" {
		command = strings.TrimSpace(profile.Command)
	}
	backend = opts.Backend
	if backend == "" {
		backend = profile.Backend
	}
	if backend == "" {
		backend = config.BackendFromCommand(command)
	}
	if backend == "" {
		backend = "claude"
	}
	return command, backend
}

func runLiveScenario(ctx context.Context, cfg *config.Config, sc Scenario, opts LiveOptions) (ScenarioVerdict, LiveRun) {
	v := ScenarioVerdict{Profile: sc.Profile, Scenario: sc.Name}
	run := LiveRun{Profile: sc.Profile, Scenario: sc.Name}
	fail := func(format string, args ...any) (ScenarioVerdict, LiveRun) {
		v.Error = fmt.Sprintf(format, args...)
		return v, run
	}
	profile, ok := cfg.Agent.Profiles[sc.Profile]
	if !ok {
		return fail("profile %q is not defined in %s", sc.Profile, opts.Workflow)
	}
	in, err := ParseInput(sc.Dir)
	if err != nil {
		return fail("input: %v", err)
	}
	command, backend := resolveCommand(opts, profile)
	if command == "" {
		return fail("profile %q has no command; pass one explicitly", sc.Profile)
	}
	now := time.Now()
	composed, err := orchestrator.ComposeEvalPrompt(orchestrator.EvalPromptInput{
		Config: cfg, ProfileName: sc.Profile, Backend: backend, Issue: in.Issue(),
		Automation: automationFor(cfg, sc.Profile, in, now), StartedAt: now,
	})
	if err != nil {
		return fail("compose prompt: %v", err)
	}
	v.PromptDigest, v.DigestSource = composed.Digest, DigestSourceLive
	root, err := os.MkdirTemp("", "itervox-eval-"+sc.Profile+"-"+sc.Name+"-*")
	if err != nil {
		return fail("sandbox: %v", err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	sb, err := newSandbox(root, in)
	if err != nil {
		return fail("sandbox: %v", err)
	}
	if backend == "claude" {
		command += " --max-turns " + strconv.Itoa(opts.Budget.MaxTurns)
	}
	runnerCommand := agent.CommandWithBackendHint(envPrefix(sb.env(in.IssueIdentifier))+command, backend)

	// #258: the profile's sandbox, as on a dispatched run; no sandbox
	// available means no eval run rather than an unconfined one.
	iso, err := agent.PrepareIsolation(config.ResolveIsolation(profile.Isolation, cfg.Agent.Isolation),
		agent.RunIsolationInputs(sb.Workspace, "", filepath.Dir(cfg.WorkflowPath), profile), nil)
	if err != nil {
		return fail("isolation: %v", err)
	}
	turnCtx, cancel := context.WithTimeout(agent.WithIsolation(ctx, iso), opts.Budget.Timeout)
	defer cancel()
	var overBudget atomic.Bool
	onProgress := func(p agent.TurnResult) {
		if usedTokens(p) > opts.Budget.MaxTokens {
			overBudget.Store(true)
			cancel()
		}
	}
	timeoutMs := int(opts.Budget.Timeout / time.Millisecond)
	// The idle read deadline sits past the wall-clock budget so a silent
	// agent is reported as the budget timeout it is.
	result, runErr := opts.Runner.RunTurn(turnCtx, slog.New(slog.NewTextHandler(opts.Log, nil)), onProgress, nil,
		composed.Text, sb.Workspace, runnerCommand, "", "", 2*timeoutMs, timeoutMs,
		agent.ParsePermissionMode(profile.PermissionMode))
	run.Tokens = usedTokens(result)
	if result.CostUSD != nil {
		run.CostUSD = *result.CostUSD
	}

	calls, readErr := os.ReadFile(sb.CallsLog)
	if readErr != nil {
		return fail("sandbox: %v", readErr)
	}
	events := sandboxEvents(parseCallsLog(string(calls)))
	meta := liveMeta(sc, backend, composed, run, now)
	runFailure := ""
	switch {
	case overBudget.Load() || run.Tokens > opts.Budget.MaxTokens:
		runFailure = fmt.Sprintf("budget: %d tokens exceeds the per-scenario cap of %d", run.Tokens, opts.Budget.MaxTokens)
	case errors.Is(turnCtx.Err(), context.DeadlineExceeded):
		runFailure = fmt.Sprintf("budget: wall-clock timeout %s exceeded", opts.Budget.Timeout)
	case runErr != nil || result.Failed:
		runFailure = "backend: " + firstNonEmpty(result.FailureText, errString(runErr), "turn failed")
	}
	// A failed run never becomes a candidate: promoting a truncated
	// transcript would lock in behaviour the agent did not finish.
	if runFailure == "" || !opts.Candidate {
		path := filepath.Join(sc.Dir, CandidateName)
		if !opts.Candidate {
			path = filepath.Join(opts.OutDir, sc.Profile, sc.Name, "recording.jsonl")
		}
		if err := writeRecording(path, meta, events, result.LastText); err != nil {
			return fail("write recording: %v", err)
		}
		run.RecordingPath = path
	}
	rec := Recording{}
	for _, e := range events {
		switch e.Event {
		case "action":
			rec.Actions = append(rec.Actions, e.Action)
		case "comment":
			rec.Comments = append(rec.Comments, e.Body)
		}
	}
	v.Deterministic = JudgeDeterministic(sc, rec)
	v.Structural = JudgeStructural(sc, rec)
	v.Error = runFailure
	return v, run
}

func usedTokens(r agent.TurnResult) int {
	return r.InputTokens + r.CachedInputTokens + r.OutputTokens
}

// envPrefix renders `KEY='value' ` assignments, sorted, for the front of a
// shell command (the same shape the worker uses for its action variables).
func envPrefix(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + agent.ShellQuote(env[k]) + " ")
	}
	return b.String()
}

// liveMeta is the provenance line a live recording starts with. Recorded
// mode skips it (unknown event); a human reviewing a candidate reads it.
func liveMeta(sc Scenario, backend string, composed orchestrator.EvalPrompt, run LiveRun, now time.Time) map[string]any {
	sum := sha256.Sum256([]byte(composed.Text))
	return map[string]any{
		"event":         "meta",
		"source":        "live",
		"profile":       sc.Profile,
		"scenario":      sc.Name,
		"backend":       backend,
		"recorded_at":   now.UTC().Format(time.RFC3339),
		"prompt_sha256": hex.EncodeToString(sum[:]),
		"prompt_digest": composed.Digest, // #201: the daemon's prompt-version digest
		"rules_hash":    composed.Rules.Hash(),
		"tokens":        run.Tokens,
		"cost_usd":      run.CostUSD,
	}
}

func writeRecording(path string, meta map[string]any, events []RecordedEvent, finalText string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(meta); err != nil {
		return err
	}
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	if strings.TrimSpace(finalText) != "" {
		// The agent's last message, for the reviewer of a candidate. Not a
		// comment: only what the agent posted through a tool is judged.
		if err := enc.Encode(RecordedEvent{Event: "result", Body: finalText}); err != nil {
			return err
		}
	}
	return atomicfs.WriteFile(path, []byte(b.String()), 0o644)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
