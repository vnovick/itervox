package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vnovick/itervox/internal/workflow"
)

var envVarRe = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)$`)

// LatestWorkflowSchemaVersion is the newest WORKFLOW.md front-matter shape
// accepted for daemon startup.
const LatestWorkflowSchemaVersion = 2

// TrackerConfig holds tracker-related configuration.
type TrackerConfig struct {
	Kind           string
	Endpoint       string
	APIKey         string
	ProjectSlug    string
	ActiveStates   []string
	TerminalStates []string
	// WorkingState is the state name to transition an issue to when it is
	// dispatched to an agent (e.g. "In Progress"). Empty string = no transition.
	WorkingState string
	// CompletionState is the state name to transition an issue to when the agent
	// finishes successfully (e.g. "In Review", "Done"). Empty string = no transition.
	// When set, the issue leaves active_states so Itervox stops re-dispatching it.
	CompletionState string
	// BacklogStates are always fetched and shown as the leftmost board column(s).
	// Defaults to ["Backlog"] for linear, [] for github.
	BacklogStates []string
	// FailedState is the state to move issues to when max retries are exhausted.
	// When empty, issues are paused instead of transitioned.
	FailedState string
}

// PollingConfig holds polling settings.
type PollingConfig struct {
	IntervalMs int
}

// WorkspaceConfig holds workspace settings.
type WorkspaceConfig struct {
	Root string
	// AutoClearWorkspace removes the cloned workspace directory after a task
	// succeeds (reaches the completion state) so disk space is reclaimed
	// automatically. Logs are kept separately and unaffected.
	AutoClearWorkspace bool
	// Worktree enables git worktree mode. When true, Itervox manages
	// per-issue git worktrees inside workspace.root instead of creating
	// one empty directory per issue. Requires the base git clone at
	// workspace.root to already exist. Default: false.
	Worktree bool
	// CloneURL is the git remote URL used to initialise the bare clone when
	// worktree mode is enabled. When empty and worktree is true, the caller
	// must ensure a git repo already exists at Root.
	CloneURL string
	// BaseBranch is the branch worktrees are created from (default: "main").
	BaseBranch string
}

// DefaultReviewerPrompt is used when reviewer_prompt is absent from WORKFLOW.md.
const DefaultReviewerPrompt = `You are an AI code reviewer for issue {{ issue.identifier }}.

Your job:
1. Run: gh pr diff to read the PR changes on branch {{ issue.branch_name }}
2. Review for: correctness, test coverage, edge cases, security issues, code style
3. If you find problems:
   - Fix them directly in the workspace
   - Push the fixes: git add -A && git commit -m "fix: reviewer corrections" && git push
   - Post a comment on the issue summarising what you fixed
   - Move issue {{ issue.identifier }} to state "Rework"
4. If the PR is clean:
   - Post an approval comment: "AI review passed ✓ — no issues found"
   - Move issue {{ issue.identifier }} to state "Merging"

Be concise in your review comments. Focus on real problems, not style nits.`

// AgentProfile holds settings for a named agent profile.
type AgentProfile struct {
	// Command overrides the default agent CLI command (e.g. "claude --model ...").
	Command string
	// Prompt is the legacy inline role description for this sub-agent.
	// Schema 2 rejects this field; it remains for legacy migration and tests.
	Prompt string
	// SoulFile is the configured path to this profile's SOUL.md file, relative
	// to WORKFLOW.md when not absolute.
	SoulFile string
	// InstructionsFile is the configured path to this profile's INSTRUCTIONS.md
	// file, relative to WORKFLOW.md when not absolute.
	InstructionsFile string
	// Soul is the loaded SOUL.md template content for this profile.
	Soul string
	// Instructions is the loaded INSTRUCTIONS.md template content for this profile.
	Instructions string
	// Backend optionally overrides runner selection when it cannot be inferred
	// from the command binary alone (for example, a wrapper script around codex).
	Backend string
	// Enabled controls whether the profile is selectable and dispatchable.
	// Nil means true for backward compatibility with older tests/config literals.
	Enabled *bool
	// AllowedActions grants the profile access to daemon-backed actions such as
	// tracker comments or provide-input. Empty = no extra actions.
	AllowedActions []string
	// CreateIssueState is the tracker state/column used when the create_issue
	// action is allowed for this profile.
	CreateIssueState string
}

// AgentConfig holds agent runner settings.
type AgentConfig struct {
	MaxConcurrentAgents        int
	MaxConcurrentAgentsByState map[string]int
	// MaxAutomationQueueLength caps durable automation dispatch entries waiting
	// for worker capacity or dependency resolution. Values <= 0 use the default
	// of 100; the queue is never unlimited.
	MaxAutomationQueueLength int
	// MaxRetryBackoffMs caps the exponential back-off between agent retries.
	// The progression is 10 s × 2^(attempt-1): 10 s, 20 s, 40 s, 80 s, 160 s,
	// then capped at MaxRetryBackoffMs for all subsequent attempts.
	// Values <= 0 fall back to the default. Retry count is controlled by
	// MaxRetries.
	// Default: 300 000 ms (5 min).
	MaxRetryBackoffMs int
	MaxTurns          int
	Command           string
	// Backend optionally overrides runner selection for the default agent command
	// when it cannot be inferred from the command string alone.
	Backend string
	// TurnTimeoutMs is the hard wall-clock limit for an entire agent session
	// (all turns combined). When the limit is exceeded the subprocess is killed
	// and the issue is scheduled for retry. Default: 3 600 000 ms (1 hour).
	// Set to 0 to disable.
	TurnTimeoutMs int
	// ReadTimeoutMs is the per-read timeout on the subprocess stdout pipe. If
	// no bytes arrive within this window the read is considered stalled and the
	// subprocess is killed. This catches hangs at the OS/pipe level before the
	// higher-level stall detector fires. Default: 30 000 ms (30 s).
	ReadTimeoutMs int
	// StallTimeoutMs is the orchestrator-level inactivity timeout. The
	// orchestrator checks every tick whether any running worker has produced an
	// SSE event within this window; if not, it cancels the worker context and
	// schedules a retry. Unlike ReadTimeoutMs (pipe-level), this operates on
	// the parsed event stream and can detect semantic stalls (e.g. the agent is
	// looping without making progress). Default: 300 000 ms (5 min).
	// Set to ≤ 0 to disable stall detection entirely.
	StallTimeoutMs int
	// SSHHosts is an optional list of "host" or "host:port" addresses.
	// When set, agent turns are executed on these hosts via SSH in order,
	// falling back to the next host on failure. Empty = run locally.
	SSHHosts []string
	// SSHHostDescriptions maps SSH host address -> optional human-readable label.
	// Keys must match entries in SSHHosts. The dashboard shows these descriptions
	// alongside the host list, and runtime edits persist them back to WORKFLOW.md.
	SSHHostDescriptions map[string]string
	// SSHStrictHostChecking is the default StrictHostKeyChecking mode applied
	// to every SSH-hosted runner command, unless overridden per-host via
	// SSHStrictHostByHost. Defaults to "accept-new" (TOFU — pin on first
	// contact, reject on mismatch). Other valid values: "yes", "no", "ask",
	// "off". Read at startup and applied via agent.SetSSHStrictHostDefault.
	// T-32.
	SSHStrictHostChecking string
	// SSHStrictHostByHost maps SSH host address -> StrictHostKeyChecking mode
	// (overrides SSHStrictHostChecking for the specific host). Useful for
	// per-host hardening (e.g. "yes" on production, "no" on a sandbox VM).
	// Applied via agent.SetSSHStrictHostOverrides. T-32.
	SSHStrictHostByHost map[string]string
	// DispatchStrategy controls how issues are routed to SSH hosts when
	// multiple are configured. Valid values: "round-robin" (default),
	// "least-loaded". Ignored when SSHHosts is empty.
	DispatchStrategy string
	// ReviewerPrompt is the Liquid template used when a reviewer worker is
	// dispatched (e.g. via the AI Review button). Falls back to DefaultReviewerPrompt.
	// Deprecated: prefer ReviewerProfile which uses the profile's own prompt.
	ReviewerPrompt string
	// ReviewerProfile is the name of the agent profile used for code review.
	// When set, the reviewer uses this profile's command, backend, and prompt
	// instead of the legacy ReviewerPrompt field. The reviewer runs as a
	// regular worker in the queue with Kind="reviewer".
	ReviewerProfile string
	// AutoReview, when true, automatically dispatches a reviewer worker
	// using ReviewerProfile after each successful worker completion.
	// Requires ReviewerProfile to be set. Default: false.
	AutoReview bool
	// Profiles is an optional map of named agent profiles. Each profile can
	// override the default agent Command. Profiles can be selected per-issue
	// from the web UI.
	Profiles map[string]AgentProfile
	// InlineInput controls whether agent input-required signals are posted as
	// tracker comments (true) or queued in the dashboard UI (false).
	// When true, the issue moves to the completion state with a question comment;
	// the user replies in the tracker and moves the issue back to continue.
	// When false (default), the dashboard shows a reply UI and posts the user's
	// response as a tracker comment before resuming the agent.
	// Default: false.
	InlineInput bool
	// MaxSwitchesPerIssuePerWindow caps how many times a `rate_limited`
	// automation can swap an issue to a different profile / backend within
	// SwitchWindowHours. Default 2. 0 means "unlimited" (not recommended).
	// Gap E.
	MaxSwitchesPerIssuePerWindow int
	// SwitchWindowHours is the rolling window over which
	// MaxSwitchesPerIssuePerWindow is counted. Default 6. Gap E.
	SwitchWindowHours int
	// SwitchRevertHours, when > 0, triggers a periodic check that reverts
	// rate_limited auto-switched profile/backend overrides whose age has
	// exceeded the TTL. 0 (default) keeps the prior behaviour: overrides
	// only clear on the next successful worker exit. Gap §6.2.
	SwitchRevertHours int
	// RateLimitErrorPatterns are case-insensitive substrings the
	// orchestrator's terminal-failure classifier matches against the
	// worker's last-error text to decide if a failure was rate-limit
	// driven (and so should fire `rate_limited` automations rather than
	// just `run_failed`). Empty → uses the built-in default list
	// (rate_limit_exceeded / rate limit / 429 / quota / too many requests).
	// Operators can extend or override the list when a vendor surfaces
	// new throttle wording. Gap §5.1.
	RateLimitErrorPatterns []string
	// MaxRetries is the maximum number of retry attempts before an issue is
	// moved to the failed state. 0 means unlimited retries (legacy behavior).
	// Default: 5.
	MaxRetries int
	// BaseBranch is the remote branch used as the base for git diffs when
	// enriching PR context (e.g. "origin/main", "origin/develop", "origin/master").
	// When empty, Itervox auto-detects via `git symbolic-ref refs/remotes/origin/HEAD`,
	// falling back to "origin/main" if detection fails.
	BaseBranch string
	// AvailableModels maps backend names ("claude", "codex") to model options
	// discovered at init time. The dashboard profile editor uses these for the
	// model dropdown. When empty, the frontend falls back to a built-in default
	// list.
	AvailableModels map[string][]ModelOption
}

// ModelOption represents an available model for a backend. Matches the
// agent.ModelOption type but lives here to avoid import cycles.
type ModelOption struct {
	ID    string `json:"id" yaml:"id"`
	Label string `json:"label" yaml:"label"`
}

// HooksConfig holds lifecycle hook settings.
type HooksConfig struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	TimeoutMs    int
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Port *int
	Host string
	// AllowUnauthenticatedLAN, when true, lets the daemon bind to a
	// non-loopback address without requiring ITERVOX_API_TOKEN. Explicit
	// opt-in for trusted networks (air-gapped LAN, behind a firewall).
	// Default false: a random token is auto-generated when binding
	// non-loopback without one.
	AllowUnauthenticatedLAN bool
}

// Config is the fully-parsed, defaulted, and resolved Itervox configuration.
type Config struct {
	// SchemaVersion is the WORKFLOW.md schema version declared by
	// itervox_schema_version. Missing versions parse as 0 and are rejected
	// by ValidateDispatch before daemon startup.
	SchemaVersion int
	// WorkflowPath is the path passed to Load, used for actionable validation
	// and migration errors.
	WorkflowPath   string
	Tracker        TrackerConfig
	Polling        PollingConfig
	Workspace      WorkspaceConfig
	Agent          AgentConfig
	Hooks          HooksConfig
	Server         ServerConfig
	Automations    []AutomationConfig
	PromptTemplate string
}

// Load reads a WORKFLOW.md file, parses front matter, applies defaults, and resolves env vars.
// It does not validate required fields. Call ValidateDispatch before starting the agent
// dispatch loop (i.e. before calling Orchestrator.Run). Utility callers that only need
// cfg.Workspace.Root, cfg.Tracker.Kind, or similar non-critical fields may omit ValidateDispatch.
func Load(path string) (*Config, error) {
	wf, err := workflow.Load(path)
	if err != nil {
		return nil, err
	}
	// Reject removed fields loudly so operators see a clear migration error
	// instead of silently-changed behavior. (`agent_mode` + `enable_agent_teams`
	// were removed in favor of always-on profile prompt + subagent roster
	// injection — see CHANGELOG.)
	if intField(wf.Config, "itervox_schema_version", 0) == LatestWorkflowSchemaVersion {
		agent := nestedMap(wf.Config, "agent")
		if _, has := agent["agent_mode"]; has {
			return nil, fmt.Errorf("config: agent.agent_mode has been removed; delete this field from WORKFLOW.md (see CHANGELOG)")
		}
		if _, has := agent["enable_agent_teams"]; has {
			return nil, fmt.Errorf("config: agent.enable_agent_teams has been removed; delete this field from WORKFLOW.md (see CHANGELOG)")
		}
	}
	cfg, err := fromWorkflow(wf, path)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// fromWorkflow builds a Config from a parsed Workflow, applying all defaults.
func fromWorkflow(wf *workflow.Workflow, workflowPath string) (*Config, error) {
	raw := wf.Config

	cfg := &Config{
		SchemaVersion:  intField(raw, "itervox_schema_version", 0),
		WorkflowPath:   workflowPath,
		PromptTemplate: wf.PromptTemplate,
	}
	if cfg.SchemaVersion > LatestWorkflowSchemaVersion {
		return nil, fmt.Errorf("unsupported itervox_schema_version %d: latest supported version is %d", cfg.SchemaVersion, LatestWorkflowSchemaVersion)
	}

	// Tracker
	tracker := nestedMap(raw, "tracker")
	cfg.Tracker.Kind = strField(tracker, "kind", "")
	// Apply the Linear default endpoint only when tracker.kind is "linear" so
	// that GitHub users who omit endpoint: get an empty string, triggering the
	// GitHub client's own default (https://api.github.com).
	defaultEndpoint := ""
	if cfg.Tracker.Kind == "linear" {
		defaultEndpoint = "https://api.linear.app/graphql"
	}
	cfg.Tracker.Endpoint = strField(tracker, "endpoint", defaultEndpoint)
	cfg.Tracker.APIKey = resolveSecret(strField(tracker, "api_key", ""))
	cfg.Tracker.ProjectSlug = strField(tracker, "project_slug", "")
	cfg.Tracker.ActiveStates = strSliceField(tracker, "active_states", []string{"Todo", "In Progress"})
	cfg.Tracker.TerminalStates = strSliceField(tracker, "terminal_states", []string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"})
	cfg.Tracker.WorkingState = strField(tracker, "working_state", "In Progress")
	cfg.Tracker.CompletionState = strField(tracker, "completion_state", "")
	defaultBacklog := []string{}
	if cfg.Tracker.Kind == "linear" {
		defaultBacklog = []string{"Backlog"}
	}
	cfg.Tracker.BacklogStates = strSliceField(tracker, "backlog_states", defaultBacklog)
	cfg.Tracker.FailedState = strField(tracker, "failed_state", "")

	// Polling
	polling := nestedMap(raw, "polling")
	cfg.Polling.IntervalMs = intField(polling, "interval_ms", 30000)

	// Workspace
	ws := nestedMap(raw, "workspace")
	defaultWSRoot := defaultWorkspaceRoot()
	cfg.Workspace.Root = resolvePathValue(strField(ws, "root", ""), defaultWSRoot)
	cfg.Workspace.AutoClearWorkspace = boolField(ws, "auto_clear", false)
	cfg.Workspace.Worktree = boolField(ws, "worktree", false)
	cfg.Workspace.CloneURL = strField(ws, "clone_url", "")
	cfg.Workspace.BaseBranch = strField(ws, "base_branch", "main")

	// Agent
	agent := nestedMap(raw, "agent")
	cfg.Agent.MaxConcurrentAgents = positiveIntField(agent, "max_concurrent_agents", 10)
	cfg.Agent.MaxAutomationQueueLength = positiveIntField(agent, "max_automation_queue_length", 100)
	cfg.Agent.MaxRetryBackoffMs = positiveIntField(agent, "max_retry_backoff_ms", 300000)
	cfg.Agent.MaxTurns = positiveIntField(agent, "max_turns", 20)
	cfg.Agent.Command = strField(agent, "command", "claude")
	cfg.Agent.Backend = strField(agent, "backend", "")
	cfg.Agent.TurnTimeoutMs = intField(agent, "turn_timeout_ms", 3600000)
	cfg.Agent.ReadTimeoutMs = positiveIntField(agent, "read_timeout_ms", 30000)
	cfg.Agent.StallTimeoutMs = intField(agent, "stall_timeout_ms", 300000)
	cfg.Agent.MaxConcurrentAgentsByState = normalizeStateLimits(mapField(agent, "max_concurrent_agents_by_state"))
	cfg.Agent.SSHHosts = strSliceField(agent, "ssh_hosts", nil)
	cfg.Agent.SSHHostDescriptions = stringMapField(agent, "ssh_host_descriptions", nil)
	// T-32: optional StrictHostKeyChecking config. Default "accept-new" (TOFU)
	// is enforced at the agent package level even if the field is omitted from
	// WORKFLOW.md, so this just lets users override the default.
	cfg.Agent.SSHStrictHostChecking = strField(agent, "ssh_strict_host_checking", "")
	cfg.Agent.SSHStrictHostByHost = stringMapField(agent, "ssh_strict_host_by_host", nil)
	cfg.Agent.DispatchStrategy = strField(agent, "dispatch_strategy", "round-robin")
	cfg.Agent.ReviewerPrompt = strField(agent, "reviewer_prompt", DefaultReviewerPrompt)
	cfg.Agent.ReviewerProfile = strField(agent, "reviewer_profile", "")
	cfg.Agent.AutoReview = boolField(agent, "auto_review", false)
	cfg.Agent.InlineInput = boolField(agent, "inline_input", false)
	cfg.Agent.MaxRetries = intField(agent, "max_retries", 5)
	// Gap E — per-issue switch cap defaults: 2 switches per 6h window.
	cfg.Agent.MaxSwitchesPerIssuePerWindow = intField(agent, "max_switches_per_issue_per_window", 2)
	// Gap §6.2 — operator-configurable TTL for auto-switched overrides.
	cfg.Agent.SwitchRevertHours = intField(agent, "switch_revert_hours", 0)
	// Gap §5.1 — operator-configurable rate-limit error-message patterns.
	cfg.Agent.RateLimitErrorPatterns = strSliceField(agent, "rate_limit_error_patterns", nil)
	cfg.Agent.SwitchWindowHours = intField(agent, "switch_window_hours", 6)
	cfg.Agent.BaseBranch = strField(agent, "base_branch", "")
	profiles, err := parseAgentProfiles(mapField(agent, "profiles"), cfg.SchemaVersion, workflowPath)
	if err != nil {
		return nil, err
	}
	cfg.Agent.Profiles = profiles
	cfg.Agent.AvailableModels = parseAvailableModels(mapField(agent, "available_models"))

	// Hooks
	hooks := nestedMap(raw, "hooks")
	cfg.Hooks.AfterCreate = strField(hooks, "after_create", "")
	cfg.Hooks.BeforeRun = strField(hooks, "before_run", "")
	cfg.Hooks.AfterRun = strField(hooks, "after_run", "")
	cfg.Hooks.BeforeRemove = strField(hooks, "before_remove", "")
	hooksTimeout := intField(hooks, "timeout_ms", 0)
	if hooksTimeout <= 0 {
		hooksTimeout = 60000
	}
	cfg.Hooks.TimeoutMs = hooksTimeout

	// Server
	srv := nestedMap(raw, "server")
	cfg.Server.Host = strField(srv, "host", "127.0.0.1")
	if p, ok := srv["port"]; ok {
		if pInt, ok := toInt(p); ok && pInt >= 0 {
			cfg.Server.Port = &pInt
		}
	}
	cfg.Server.AllowUnauthenticatedLAN = boolField(srv, "allow_unauthenticated_lan", false)
	cfg.Automations = parseAutomations(raw["automations"])
	if len(cfg.Automations) == 0 {
		legacy := legacySchedulesToAutomations(parseSchedules(raw["schedules"]))
		if len(legacy) > 0 {
			slog.Warn("config: schedules: is deprecated; migrate to automations:", "count", len(legacy))
		}
		cfg.Automations = legacy
	}

	return cfg, nil
}

// resolveSecret resolves $VAR_NAME references for secret fields.
// Returns the resolved value, or empty string if unresolvable.
func resolveSecret(value string) string {
	if m := envVarRe.FindStringSubmatch(value); m != nil {
		return os.Getenv(m[1])
	}
	return value
}

// resolvePathValue resolves $VAR and ~ for path fields.
func resolvePathValue(value, defaultVal string) string {
	if value == "" {
		return defaultVal
	}
	// $VAR resolution
	if m := envVarRe.FindStringSubmatch(value); m != nil {
		resolved := os.Getenv(m[1])
		if resolved == "" {
			return defaultVal
		}
		return expandTilde(resolved)
	}
	expanded := expandTilde(value)
	if expanded == "" {
		return defaultVal
	}
	return expanded
}

// defaultWorkspaceRoot returns ~/.itervox/workspaces, falling back to
// os.TempDir()/itervox_workspaces if the home directory cannot be determined.
func defaultWorkspaceRoot() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".itervox", "workspaces")
	}
	return filepath.Join(os.TempDir(), "itervox_workspaces")
}

func expandTilde(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			slog.Warn("config: cannot expand ~, using path as-is", "path", path, "error", err)
			return path // return unexpanded path rather than silently returning ""
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// normalizeStateLimits lowercases state keys and drops invalid (non-positive) entries.
func normalizeStateLimits(raw map[string]any) map[string]int {
	result := make(map[string]int)
	for k, v := range raw {
		normalized := strings.ToLower(k)
		if normalized == "" {
			continue
		}
		if n, ok := toInt(v); ok && n > 0 {
			result[normalized] = n
		}
	}
	return result
}

// parseAgentProfiles parses the agent.profiles map from YAML into a
// map[string]AgentProfile. Unknown or invalid legacy entries are silently skipped.
func parseAgentProfiles(raw map[string]any, schemaVersion int, workflowPath string) (map[string]AgentProfile, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	profiles := make(map[string]AgentProfile, len(raw))
	for name, v := range raw {
		m := nestedMap(map[string]any{name: v}, name)
		if schemaVersion == LatestWorkflowSchemaVersion {
			if _, hasPrompt := m["prompt"]; hasPrompt {
				return nil, fmt.Errorf("%s", LegacyInlineProfilePromptMessage(workflowPath))
			}
			soulFile := strField(m, "soul_file", "")
			instructionsFile := strField(m, "instructions_file", "")
			if soulFile == "" || instructionsFile == "" {
				return nil, fmt.Errorf("config: agent.profiles.%s requires soul_file and instructions_file in schema %d", name, LatestWorkflowSchemaVersion)
			}
			soul, err := readProfileTemplateFile(workflowPath, soulFile)
			if err != nil {
				return nil, fmt.Errorf("config: agent.profiles.%s.soul_file: %w", name, err)
			}
			instructions, err := readProfileTemplateFile(workflowPath, instructionsFile)
			if err != nil {
				return nil, fmt.Errorf("config: agent.profiles.%s.instructions_file: %w", name, err)
			}
			cmd := strField(m, "command", "")
			if cmd == "" {
				return nil, fmt.Errorf("config: agent.profiles.%s.command is required in schema %d", name, LatestWorkflowSchemaVersion)
			}
			profiles[name] = AgentProfile{
				Command:          cmd,
				SoulFile:         soulFile,
				InstructionsFile: instructionsFile,
				Soul:             soul,
				Instructions:     instructions,
				Backend:          strField(m, "backend", ""),
				Enabled:          boolPtr(boolField(m, "enabled", true)),
				AllowedActions:   NormalizeAllowedActions(strSliceField(m, "allowed_actions", nil)),
				CreateIssueState: strField(m, "create_issue_state", ""),
			}
			continue
		}
		cmd := strField(m, "command", "")
		if cmd == "" {
			continue
		}
		profiles[name] = AgentProfile{
			Command:          cmd,
			Prompt:           strField(m, "prompt", ""),
			Backend:          strField(m, "backend", ""),
			Enabled:          boolPtr(boolField(m, "enabled", true)),
			AllowedActions:   NormalizeAllowedActions(strSliceField(m, "allowed_actions", nil)),
			CreateIssueState: strField(m, "create_issue_state", ""),
		}
	}
	if len(profiles) == 0 {
		return nil, nil
	}
	return profiles, nil
}

func readProfileTemplateFile(workflowPath, configuredPath string) (string, error) {
	resolved := resolveWorkflowRelativeFile(workflowPath, configuredPath)
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", resolved, err)
	}
	return strings.TrimSpace(string(data)), nil
}

func resolveWorkflowRelativeFile(workflowPath, configuredPath string) string {
	if filepath.IsAbs(configuredPath) {
		return filepath.Clean(configuredPath)
	}
	base := "."
	if workflowPath != "" {
		base = filepath.Dir(workflowPath)
	}
	return filepath.Clean(filepath.Join(base, configuredPath))
}

func boolPtr(v bool) *bool {
	return &v
}

func ProfileEnabled(profile AgentProfile) bool {
	return profile.Enabled == nil || *profile.Enabled
}

// parseAvailableModels parses the agent.available_models YAML field.
// Expected format:
//
//	available_models:
//	  claude:
//	    - { id: "claude-sonnet-4-6", label: "Sonnet 4.6" }
//	  codex:
//	    - { id: "gpt-5.2-codex", label: "GPT-5.2 Codex" }
func parseAvailableModels(raw map[string]any) map[string][]ModelOption {
	if len(raw) == 0 {
		return nil
	}
	result := make(map[string][]ModelOption, len(raw))
	for backend, v := range raw {
		items, ok := v.([]any)
		if !ok {
			continue
		}
		models := make([]ModelOption, 0, len(items))
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id, _ := m["id"].(string)
			label, _ := m["label"].(string)
			if id == "" {
				continue
			}
			if label == "" {
				label = id
			}
			models = append(models, ModelOption{ID: id, Label: label})
		}
		if len(models) > 0 {
			result[backend] = models
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// --- helpers ---

func nestedMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	v, ok := m[key]
	if !ok {
		return map[string]any{}
	}
	switch cast := v.(type) {
	case map[string]any:
		return cast
	case map[any]any:
		out := make(map[string]any, len(cast))
		for k, val := range cast {
			out[fmt.Sprintf("%v", k)] = val
		}
		return out
	}
	return map[string]any{}
}

func mapField(m map[string]any, key string) map[string]any {
	return nestedMap(m, key)
}

func stringMapField(m map[string]any, key string, defaultVal map[string]string) map[string]string {
	if m == nil {
		return defaultVal
	}
	raw := mapField(m, key)
	if len(raw) == 0 {
		return defaultVal
	}
	out := make(map[string]string, len(raw))
	for mapKey, value := range raw {
		if value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			out[mapKey] = typed
		default:
			out[mapKey] = fmt.Sprintf("%v", typed)
		}
	}
	return out
}

func strField(m map[string]any, key, defaultVal string) string {
	if m == nil {
		return defaultVal
	}
	v, ok := m[key]
	if !ok || v == nil {
		return defaultVal
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return defaultVal
	}
	return s
}

func intField(m map[string]any, key string, defaultVal int) int {
	if m == nil {
		return defaultVal
	}
	v, ok := m[key]
	if !ok || v == nil {
		return defaultVal
	}
	n, ok := toInt(v)
	if !ok {
		return defaultVal
	}
	return n
}

func boolField(m map[string]any, key string, defaultVal bool) bool {
	if m == nil {
		return defaultVal
	}
	v, ok := m[key]
	if !ok || v == nil {
		return defaultVal
	}
	b, ok := v.(bool)
	if !ok {
		return defaultVal
	}
	return b
}

func positiveIntField(m map[string]any, key string, defaultVal int) int {
	n := intField(m, key, 0)
	if n <= 0 {
		return defaultVal
	}
	return n
}

func strSliceField(m map[string]any, key string, defaultVal []string) []string {
	if m == nil {
		return defaultVal
	}
	v, ok := m[key]
	if !ok || v == nil {
		return defaultVal
	}
	raw, ok := v.([]any)
	if !ok {
		return defaultVal
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return defaultVal
	}
	return result
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
