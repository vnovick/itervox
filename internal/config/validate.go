package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/osteele/liquid"
	"github.com/vnovick/itervox/internal/schedule"
)

// supportedTrackerKinds includes "memory" so the embedded quickstart template
// (templates.Quickstart) — which replaces the former --demo flag — passes
// ValidateDispatch. The memory tracker is otherwise a real, internal-only
// codepath; allowing it in config simply removes the artificial gate.
var supportedTrackerKinds = map[string]bool{
	"linear": true,
	"github": true,
	"memory": true,
}

// ErrAutoClearAutoReviewConflict reports that workspace cleanup and automatic
// reviewer dispatch were enabled together in a way that would race.
var ErrAutoClearAutoReviewConflict = errors.New("workspace.auto_clear and agent.auto_review cannot both be enabled")

// ErrAutoReviewRequiresReviewerProfile reports that automatic review was
// enabled without a configured reviewer profile.
var ErrAutoReviewRequiresReviewerProfile = errors.New("agent.auto_review requires agent.reviewer_profile to be set")

// ErrReviewerProfileNotFound reports that a configured reviewer profile does
// not exist in agent.profiles.
var ErrReviewerProfileNotFound = errors.New("agent.reviewer_profile must reference an existing profile")

// ErrReviewerProfileDisabled reports that a configured reviewer profile exists
// but is disabled.
var ErrReviewerProfileDisabled = errors.New("agent.reviewer_profile must reference an enabled profile")

// ErrDepsAnalyzerProfileNotFound reports that a configured dependency-analyzer
// profile does not exist in agent.profiles.
var ErrDepsAnalyzerProfileNotFound = errors.New("agent.deps_analyzer_profile must reference an existing profile")

// ErrDepsAnalyzerProfileDisabled reports that a configured dependency-analyzer
// profile exists but is disabled.
var ErrDepsAnalyzerProfileDisabled = errors.New("agent.deps_analyzer_profile must reference an enabled profile")

func workflowUpdatePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "WORKFLOW.md"
	}
	return path
}

// LegacyInlineProfilePromptMessage returns the canonical migration guidance
// used when a workflow is still on the pre-schema-2 inline profile prompt shape.
func LegacyInlineProfilePromptMessage(workflowPath string) string {
	return fmt.Sprintf(`WORKFLOW.md uses the legacy inline profile-prompt schema.
Run:
  itervox init --update --workflow %s

This creates .itervox/agents/<profile>/SOUL.md and INSTRUCTIONS.md,
rewrites profile references, and keeps a WORKFLOW.md.bak backup.`, workflowUpdatePath(workflowPath))
}

// MissingWorkflowSchemaMessage returns the canonical migration guidance used
// when a workflow lacks an explicit itervox_schema_version marker.
func MissingWorkflowSchemaMessage(workflowPath string) string {
	return fmt.Sprintf(`WORKFLOW.md is missing itervox_schema_version.
Run:
  itervox init --update --workflow %s

This creates .itervox/agents/<profile>/SOUL.md and INSTRUCTIONS.md,
rewrites profile references, and keeps a WORKFLOW.md.bak backup.`, workflowUpdatePath(workflowPath))
}

// ValidateWorkflowSchema rejects workflows that are not on the latest supported
// schema before the daemon starts dispatching agents.
func ValidateWorkflowSchema(cfg *Config) error {
	switch {
	case cfg.SchemaVersion == 0:
		return errors.New(MissingWorkflowSchemaMessage(cfg.WorkflowPath))
	case cfg.SchemaVersion < LatestWorkflowSchemaVersion:
		return errors.New(LegacyInlineProfilePromptMessage(cfg.WorkflowPath))
	case cfg.SchemaVersion > LatestWorkflowSchemaVersion:
		return fmt.Errorf("unsupported itervox_schema_version %d: latest supported version is %d", cfg.SchemaVersion, LatestWorkflowSchemaVersion)
	default:
		return nil
	}
}

// ValidateReviewerAutoReview rejects configurations where auto-review was
// enabled without a reviewer profile to dispatch.
func ValidateReviewerAutoReview(reviewerProfile string, autoReview bool) error {
	if autoReview && strings.TrimSpace(reviewerProfile) == "" {
		return fmt.Errorf("%w: set agent.reviewer_profile or disable agent.auto_review", ErrAutoReviewRequiresReviewerProfile)
	}
	return nil
}

// ValidateAutoClearAutoReview is retained for API compatibility with callers
// that branched on ErrAutoClearAutoReviewConflict. Under the legacy semantics
// the two settings raced — `auto_clear` removed the workspace immediately on
// worker success, leaving nothing for the reviewer to inspect. The v0.2.0
// semantic change (workspace clears only on terminal tracker states) defers
// the clear until after the reviewer also completes, so the two settings now
// safely coexist. This function is a no-op kept to avoid breaking external
// consumers; new code should not call it.
func ValidateAutoClearAutoReview(autoClear bool, reviewerProfile string, autoReview bool) error {
	_ = autoClear
	_ = reviewerProfile
	_ = autoReview
	return nil
}

func ValidateReviewerProfile(profiles map[string]AgentProfile, reviewerProfile string) error {
	reviewerProfile = strings.TrimSpace(reviewerProfile)
	if reviewerProfile == "" {
		return nil
	}
	profile, ok := profiles[reviewerProfile]
	if !ok {
		return fmt.Errorf("%w: %q", ErrReviewerProfileNotFound, reviewerProfile)
	}
	if !ProfileEnabled(profile) {
		return fmt.Errorf("%w: %q", ErrReviewerProfileDisabled, reviewerProfile)
	}
	return nil
}

// ValidateDepsAnalyzerProfile rejects configurations whose
// agent.deps_analyzer_profile names a profile that is missing or disabled.
// Empty is accepted (analyzer simply disabled).
func ValidateDepsAnalyzerProfile(profiles map[string]AgentProfile, depsAnalyzerProfile string) error {
	depsAnalyzerProfile = strings.TrimSpace(depsAnalyzerProfile)
	if depsAnalyzerProfile == "" {
		return nil
	}
	profile, ok := profiles[depsAnalyzerProfile]
	if !ok {
		return fmt.Errorf("%w: %q", ErrDepsAnalyzerProfileNotFound, depsAnalyzerProfile)
	}
	if !ProfileEnabled(profile) {
		return fmt.Errorf("%w: %q", ErrDepsAnalyzerProfileDisabled, depsAnalyzerProfile)
	}
	return nil
}

// ValidateDispatch runs the spec §6.3 dispatch preflight checks against an
// already-loaded Config. Call Load first; this function does not re-read the file.
func ValidateDispatch(cfg *Config) error {
	if err := ValidateWorkflowSchema(cfg); err != nil {
		return err
	}

	// Check 1: tracker.kind present and supported
	if cfg.Tracker.Kind == "" {
		return fmt.Errorf("missing tracker.kind: must be one of: linear, github")
	}
	if !supportedTrackerKinds[cfg.Tracker.Kind] {
		return fmt.Errorf("unsupported_tracker_kind: %q (must be linear or github)", cfg.Tracker.Kind)
	}

	// Check 3: tracker.api_key present after $VAR resolution.
	// The memory tracker is internal-only and needs no credentials, so this
	// gate only applies to remote trackers (linear, github).
	if cfg.Tracker.Kind != "memory" && cfg.Tracker.APIKey == "" {
		return fmt.Errorf("missing tracker.api_key: must be set or resolved from $VAR")
	}

	// Check 4: tracker.project_slug present (required for GitHub; optional for Linear)
	if cfg.Tracker.Kind == "github" && cfg.Tracker.ProjectSlug == "" {
		return fmt.Errorf("missing tracker.project_slug: required for GitHub (owner/repo)")
	}

	// Check 5: agent.command present and non-empty
	if cfg.Agent.Command == "" {
		return fmt.Errorf("missing agent.command: must be non-empty (default: claude)")
	}

	// Check 6: reviewer_prompt is a valid Liquid template (if non-empty and not the default)
	if rp := cfg.Agent.ReviewerPrompt; rp != "" {
		eng := liquid.NewEngine()
		if _, err := eng.ParseTemplate([]byte(rp)); err != nil {
			return fmt.Errorf("agent.reviewer_prompt: invalid Liquid template: %w", err)
		}
	}

	// Check 7: ssh_hosts must not start with '-' or contain whitespace (prevents SSH flag injection)
	for _, host := range cfg.Agent.SSHHosts {
		if strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \t") {
			return fmt.Errorf("invalid ssh host %q: must not start with '-' or contain whitespace", host)
		}
	}
	// Check 7b: StrictHostKeyChecking modes must be ones ssh accepts
	// (CORE-140) — an invalid value used to be dropped silently at startup.
	if err := ValidateSSHStrictHostChecking(cfg.Agent.SSHStrictHostChecking, cfg.Agent.SSHStrictHostByHost); err != nil {
		return err
	}

	// Check 8: profile commands must not contain shell metacharacters.
	// Profile commands are passed as the first argument to bash -lc, so
	// unescaped `;`, `|`, `&`, `` ` ``, `$`, `(`, `)`, `<`, `>` allow
	// shell code injection via a crafted WORKFLOW.md. Commands are user-
	// supplied from WORKFLOW.md, but a clear validation error is better than
	// a silent foot-gun.
	const shellMetachars = ";|&`$()><"
	for name, profile := range cfg.Agent.Profiles {
		if strings.ContainsAny(profile.Command, shellMetachars) {
			return fmt.Errorf("invalid profile %q: command %q contains shell metacharacters (%s); use a wrapper script instead",
				name, profile.Command, shellMetachars)
		}
	}
	if err := ValidateAgentProfiles(cfg.Agent.Profiles); err != nil {
		return err
	}
	if err := ValidateAutomationsWithDefaults(cfg.Automations, cfg.Agent.Profiles, cfg.Agent.Command); err != nil {
		return err
	}

	if err := ValidateBackendFallback(cfg.Agent.BackendFallback, cfg.Agent.Profiles, cfg.Agent.Command); err != nil {
		return err
	}
	if err := ValidateReviewerAutoReview(cfg.Agent.ReviewerProfile, cfg.Agent.AutoReview); err != nil {
		return err
	}
	if err := ValidateReviewerProfile(cfg.Agent.Profiles, cfg.Agent.ReviewerProfile); err != nil {
		return err
	}
	if err := ValidateDepsAnalyzerProfile(cfg.Agent.Profiles, cfg.Agent.DepsAnalyzerProfile); err != nil {
		return err
	}
	if err := ValidateAutoClearAutoReview(
		cfg.Workspace.AutoClearWorkspace,
		cfg.Agent.ReviewerProfile,
		cfg.Agent.AutoReview,
	); err != nil {
		return err
	}

	return nil
}

func ValidateAgentProfiles(profiles map[string]AgentProfile) error {
	for name, profile := range profiles {
		actions := NormalizeAllowedActions(profile.AllowedActions)
		if slices.Contains(actions, AgentActionCreateIssue) && strings.TrimSpace(profile.CreateIssueState) == "" {
			return fmt.Errorf("invalid profile %q: create_issue_state is required when create_issue is enabled", name)
		}
	}
	return nil
}

// ValidateAutomationsWithDefaults is ValidateAutomations plus the default
// agent command, used to resolve the effective command of a switch profile
// whose own command is empty (CORE-010). An empty defaultCommand disables only
// the inherited-command half of the switch_to_backend check.
func ValidateAutomationsWithDefaults(automations []AutomationConfig, profiles map[string]AgentProfile, defaultCommand string) error {
	if len(automations) == 0 {
		return nil
	}
	seenIDs := make(map[string]struct{}, len(automations))
	for _, entry := range automations {
		id := strings.TrimSpace(entry.ID)
		if id == "" || strings.TrimSpace(entry.Profile) == "" || strings.TrimSpace(entry.Trigger.Type) == "" {
			return fmt.Errorf("each automation requires id, trigger.type, and profile")
		}
		key := strings.ToLower(id)
		if _, exists := seenIDs[key]; exists {
			return fmt.Errorf("duplicate automation id %q", id)
		}
		seenIDs[key] = struct{}{}
	}
	for _, entry := range automations {
		id := strings.TrimSpace(entry.ID)
		profileName := strings.TrimSpace(entry.Profile)
		triggerType := strings.TrimSpace(entry.Trigger.Type)

		// T-42 (06.G-02): the unknown-profile check fires regardless of the
		// automation's enabled flag — a disabled-but-misconfigured rule would
		// otherwise slip through and crash dispatch the moment a user
		// re-enabled it. The disabled-profile check is only applied when the
		// automation itself is enabled, because UpsertProfile's cascade
		// deliberately leaves a disabled automation pointing at a
		// disabled profile in lock-step (re-enabling either side without
		// fixing the other would fail this same check on the next save).
		profile, ok := profiles[profileName]
		if !ok {
			return fmt.Errorf("automation %q references unknown profile %q", id, profileName)
		}
		if entry.Enabled && !ProfileEnabled(profile) {
			return fmt.Errorf("automation %q references disabled profile %q", id, profileName)
		}

		switch triggerType {
		case AutomationTriggerCron:
			if strings.TrimSpace(entry.Trigger.Cron) == "" {
				return fmt.Errorf("automation %q: cron automations require trigger.cron", id)
			}
			if _, err := schedule.Parse(entry.Trigger.Cron); err != nil {
				return fmt.Errorf("automation %q invalid cron: %w", id, err)
			}
			if entry.Trigger.Timezone != "" {
				if _, err := time.LoadLocation(entry.Trigger.Timezone); err != nil {
					return fmt.Errorf("automation %q invalid timezone: %w", id, err)
				}
			}
		case AutomationTriggerInputRequired:
		case AutomationTriggerTrackerComment:
		case AutomationTriggerIssueMovedBacklog:
		case AutomationTriggerRunFailed:
		case AutomationTriggerPROpened, AutomationTriggerPRMerged:
		case AutomationTriggerBlockersResolved:
			moveToState := strings.TrimSpace(entry.Policy.MoveToState)
			if moveToState != "" {
				if !slices.Contains(NormalizeAllowedActions(profile.AllowedActions), AgentActionMoveState) {
					return fmt.Errorf("automation %q: policy.move_to_state requires profile %q to allow %q", id, profileName, AgentActionMoveState)
				}
			}
		case AutomationTriggerRateLimited:
			// Gap E — switch_to_profile is required; switch_to_backend is
			// optional but if set must name a known backend. cooldown_minutes
			// must be non-negative.
			switchToProfile := strings.TrimSpace(entry.Policy.SwitchToProfile)
			if switchToProfile == "" {
				return fmt.Errorf("automation %q: rate_limited automations require policy.switch_to_profile", id)
			}
			switchProfile, ok := profiles[switchToProfile]
			if !ok {
				return fmt.Errorf("automation %q references unknown switch_to_profile %q", id, switchToProfile)
			}
			if entry.Enabled && !ProfileEnabled(switchProfile) {
				return fmt.Errorf("automation %q references disabled switch_to_profile %q", id, switchToProfile)
			}
			if !entry.Policy.AutoResume {
				return fmt.Errorf("automation %q: rate_limited automations require policy.auto_switch: true or policy.auto_resume: true", id)
			}
			switch entry.Policy.SwitchToBackend {
			case "", "claude", "codex":
			default:
				return fmt.Errorf("automation %q: policy.switch_to_backend must be empty, \"claude\", or \"codex\"", id)
			}
			if err := validateSwitchBackendCommand(id, switchToProfile, switchProfile, entry.Policy.SwitchToBackend, defaultCommand); err != nil {
				return err
			}
			if entry.Policy.CooldownMinutes < 0 {
				return fmt.Errorf("automation %q: policy.cooldown_minutes must be >= 0", id)
			}
		case AutomationTriggerIssueEnteredState:
			if strings.TrimSpace(entry.Trigger.State) == "" {
				return fmt.Errorf("automation %q: issue_entered_state automations require trigger.state", id)
			}
		default:
			return fmt.Errorf("automation %q has unsupported trigger type %q", id, triggerType)
		}
		// Gap E — switch_to_profile / switch_to_backend / cooldown_minutes
		// only make sense on rate_limited triggers.
		if triggerType != AutomationTriggerRateLimited {
			if strings.TrimSpace(entry.Policy.SwitchToProfile) != "" {
				return fmt.Errorf("automation %q: policy.switch_to_profile is only meaningful on rate_limited triggers", id)
			}
			if strings.TrimSpace(entry.Policy.SwitchToBackend) != "" {
				return fmt.Errorf("automation %q: policy.switch_to_backend is only meaningful on rate_limited triggers", id)
			}
			if entry.Policy.CooldownMinutes > 0 {
				return fmt.Errorf("automation %q: policy.cooldown_minutes is only meaningful on rate_limited triggers", id)
			}
		}
		if triggerType != AutomationTriggerBlockersResolved && strings.TrimSpace(entry.Policy.MoveToState) != "" {
			return fmt.Errorf("automation %q: policy.move_to_state is only meaningful on blockers_resolved triggers", id)
		}
		if entry.Filter.MatchMode != "" &&
			entry.Filter.MatchMode != AutomationFilterMatchAll &&
			entry.Filter.MatchMode != AutomationFilterMatchAny {
			return fmt.Errorf("automation %q filter.match_mode must be %q or %q", id, AutomationFilterMatchAll, AutomationFilterMatchAny)
		}
		if entry.Filter.Limit < 0 {
			return fmt.Errorf("automation %q filter.limit must be >= 0", id)
		}
		if entry.Filter.MaxAgeMinutes < 0 {
			return fmt.Errorf("automation %q filter.max_age_minutes must be >= 0", id)
		}
		if entry.Filter.MaxAgeMinutes > 0 && triggerType != AutomationTriggerInputRequired {
			return fmt.Errorf("automation %q filter.max_age_minutes is only meaningful on input_required triggers", id)
		}
		if entry.Filter.IdentifierRegex != "" {
			if _, err := regexp.Compile(entry.Filter.IdentifierRegex); err != nil {
				return fmt.Errorf("automation %q invalid identifier_regex: %w", id, err)
			}
		}
		if entry.Filter.InputContextRegex != "" {
			if _, err := regexp.Compile(entry.Filter.InputContextRegex); err != nil {
				return fmt.Errorf("automation %q invalid input_context_regex: %w", id, err)
			}
		}
		if entry.Filter.BodyRegex != "" {
			if _, err := regexp.Compile(entry.Filter.BodyRegex); err != nil {
				return fmt.Errorf("automation %q invalid body_regex: %w", id, err)
			}
		}
		// body_contains / body_regex are only meaningful on triggers that
		// carry a comment body (currently tracker_comment_added). On other
		// triggers they're silently ignored at match time but flagged here
		// so operators see their mistake at startup.
		if (len(entry.Filter.BodyContains) > 0 || entry.Filter.BodyRegex != "") &&
			triggerType != AutomationTriggerTrackerComment {
			return fmt.Errorf("automation %q: filter.body_contains and filter.body_regex are only meaningful on tracker_comment_added triggers", id)
		}
	}
	return nil
}

// ValidateBackendFallback validates agent.backend_fallback (CORE-054):
// chain entries are known, distinct backends; switch_back and on_unmapped are
// known policies; the minutes are in range; every profile_map source is a
// known profile (or "default"), every backend key is a known backend, and
// every target exists, is enabled and actually runs that backend — its
// profile backend, else its command's binary, else agent.command's. A target
// whose backend cannot be determined (an opaque wrapper with no backend:) is
// rejected, because the orchestrator would refuse to pair it (CORE-115).
// A disabled block (enabled: false) is not validated.
func ValidateBackendFallback(fb BackendFallbackConfig, profiles map[string]AgentProfile, defaultCommand string) error {
	if !fb.Enabled {
		return nil
	}
	seen := make(map[string]struct{}, len(fb.Chain))
	for _, b := range fb.Chain {
		if !IsSupportedBackend(b) {
			return fmt.Errorf("agent.backend_fallback.chain: unknown backend %q (must be claude or codex)", b)
		}
		if _, dup := seen[b]; dup {
			return fmt.Errorf("agent.backend_fallback.chain: backend %q listed twice", b)
		}
		seen[b] = struct{}{}
	}
	switch fb.SwitchBack {
	case BackendFallbackSwitchBackAtReset, BackendFallbackSwitchBackOnSuccess, BackendFallbackSwitchBackManual:
	default:
		return fmt.Errorf("agent.backend_fallback.switch_back: %q must be at_reset, on_success or manual", fb.SwitchBack)
	}
	switch fb.OnUnmapped {
	case BackendFallbackUnmappedHold, BackendFallbackUnmappedBackendHint:
	default:
		return fmt.Errorf("agent.backend_fallback.on_unmapped: %q must be hold or backend_hint", fb.OnUnmapped)
	}
	if fb.MinDwellMinutes < 0 {
		return fmt.Errorf("agent.backend_fallback.min_dwell_minutes must be >= 0")
	}
	if fb.DefaultCooldownMinutes <= 0 || fb.DefaultCooldownMinutes > MaxBackendFallbackCooldownMinutes {
		return fmt.Errorf("agent.backend_fallback.default_cooldown_minutes must be between 1 and %d (the breaker never holds an unknown reset longer than a day)", MaxBackendFallbackCooldownMinutes)
	}
	sources := make([]string, 0, len(fb.ProfileMap))
	for source := range fb.ProfileMap {
		sources = append(sources, source)
	}
	slices.Sort(sources)
	for _, source := range sources {
		if _, ok := profiles[source]; !ok && source != BackendFallbackDefaultProfileKey {
			return fmt.Errorf("agent.backend_fallback.profile_map: unknown source profile %q", source)
		}
		row := fb.ProfileMap[source]
		backends := make([]string, 0, len(row))
		for b := range row {
			backends = append(backends, b)
		}
		slices.Sort(backends)
		for _, backend := range backends {
			target := row[backend]
			where := fmt.Sprintf("agent.backend_fallback.profile_map.%s.%s", source, backend)
			if !IsSupportedBackend(backend) {
				return fmt.Errorf("%s: backend key %q must be claude or codex", where, backend)
			}
			profile, ok := profiles[target]
			if !ok {
				return fmt.Errorf("%s: unknown profile %q", where, target)
			}
			if !ProfileEnabled(profile) {
				return fmt.Errorf("%s: disabled profile %q", where, target)
			}
			got := ProfileRunsBackend(profile, defaultCommand)
			if got == "" {
				return fmt.Errorf("%s: cannot tell which backend profile %q runs; set backend: %s on it", where, target, backend)
			}
			if got != backend {
				return fmt.Errorf("%s: profile %q runs %q, not %q", where, target, got, backend)
			}
		}
	}
	return nil
}

// ProfileRunsBackend is the backend a profile's runs execute on: the
// recognisable binary (or backend hint) of its command — else of
// defaultCommand, which an empty profile command inherits — and only for an
// opaque wrapper its explicit backend. A recognisable binary wins over
// backend:, as at dispatch (CORE-115). "" when unknown.
func ProfileRunsBackend(profile AgentProfile, defaultCommand string) string {
	command := profile.Command
	if strings.TrimSpace(command) == "" {
		command = defaultCommand
	}
	if bin := BackendFromCommand(command); bin != "" {
		return bin
	}
	if IsSupportedBackend(profile.Backend) {
		return profile.Backend
	}
	return ""
}
