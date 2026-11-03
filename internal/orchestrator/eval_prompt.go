package orchestrator

import (
	"cmp"
	"maps"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/prompt"
	"github.com/vnovick/itervox/internal/rules"
)

// profileTailInput is what profilePromptTail needs, already snapshotted by
// the caller (runWorker reads cfg under cfgMu before the turn loop).
type profileTailInput struct {
	Rules         rules.Set
	Template      string // the run's base template (WORKFLOW.md body or reviewer_prompt)
	ProfileName   string
	Profiles      map[string]config.AgentProfile
	Automation    *AutomationDispatch
	Issue         domain.Issue
	Attempt       *int
	RunVars       map[string]any
	ActionContext string
	Backend       string
}

// profilePromptTail renders the profile-dependent tail of a worker prompt,
// each block preceded by a blank line: the #157 `## Rules` block (unless one
// of the run's templates places the rules itself), the profile's SOUL.md and
// INSTRUCTIONS.md, the automation's instructions, the daemon action notice
// and the sub-agent roster. runWorker and ComposeEvalPrompt (#189) share it,
// so a live eval judges the prompt a dispatched worker actually receives.
func profilePromptTail(in profileTailInput) string {
	var b strings.Builder
	add := func(block string) {
		if block != "" {
			b.WriteString("\n\n")
			b.WriteString(block)
		}
	}
	// #157: the rules block, ahead of SOUL.md and INSTRUCTIONS.md, unless
	// one of the run's templates places the rules itself.
	ruleTemplates := []string{in.Template}
	if p, ok := in.Profiles[in.ProfileName]; ok && in.ProfileName != "" {
		ruleTemplates = append(ruleTemplates, p.Soul, p.Instructions, p.Prompt)
	}
	if in.Automation != nil {
		ruleTemplates = append(ruleTemplates, in.Automation.Instructions)
	}
	add(rulesPromptBlock(in.Rules, ruleTemplates...))

	// Append the active profile's prompt (role context) whenever a named
	// profile is selected. The pre-removal `agent_mode == "teams"` gate
	// has been deleted (agent_mode is gone — see CHANGELOG); the
	// subagent roster context below also injects unconditionally so
	// multi-profile setups always tell the agent who its peers are.
	if in.ProfileName != "" {
		if profile, ok := in.Profiles[in.ProfileName]; ok {
			for _, block := range renderProfilePromptBlocks(profile, in.Issue, in.Attempt, in.RunVars) {
				add(block)
			}
		}
	}
	if in.Automation != nil && in.Automation.Instructions != "" {
		bindings := automationTriggerBindings(in.Automation)
		maps.Copy(bindings, in.RunVars)
		add(prompt.RenderPromptOverlay(in.Automation.Instructions, in.Issue, in.Attempt, bindings))
	}
	add(in.ActionContext)
	// Sub-agent roster context whenever the inventory has more than one
	// profile — gives the active backend the names + descriptions of peer
	// agents it can spawn via its delegation tool. `buildSubAgentContext`
	// returns "" when there's nothing to say, so single-profile setups see
	// no change.
	add(buildSubAgentContext(in.Profiles, in.ProfileName, in.Backend))
	return b.String()
}

// EvalPromptInput describes one offline run for ComposeEvalPrompt.
type EvalPromptInput struct {
	// Config is the loaded WORKFLOW.md whose profiles, prompt template and
	// rules layers the run uses.
	Config *config.Config
	// ProfileName names the profile under evaluation (must exist in Config).
	ProfileName string
	// Backend is the backend the run targets ("claude" when empty); it scopes
	// the rules and names the sub-agent tool.
	Backend string
	// Issue is the scenario's issue, comments included.
	Issue domain.Issue
	// Automation, when non-nil, is the automation dispatch the run stands
	// in for: its instructions and trigger bindings render as they would for
	// a real automation run, and it narrows the profile's allowed actions.
	Automation *AutomationDispatch
	// StartedAt stamps the run's handoff path; zero means now.
	StartedAt time.Time
}

// EvalPrompt is a composed prompt plus the rule set it embeds.
type EvalPrompt struct {
	Text  string
	Rules rules.Set
}

// ComposeEvalPrompt renders the first-turn prompt a fresh local dispatch of
// in.ProfileName would receive (#189): the WORKFLOW.md body, the operator
// reply envelope, the run context, the evidence block, and the shared
// profilePromptTail (rules, SOUL.md, INSTRUCTIONS.md, automation instructions,
// action notice, sub-agent roster). It runs no worker, touches no tracker
// and needs no Orchestrator; the eval live mode calls it so it judges the
// prompt the daemon would send rather than a hand-maintained copy.
//
// Sections that depend on a real workspace or tracker (prior handoffs, the
// open-PR context, stacked-PR and review-diff blocks) are omitted: the
// eval's sandbox supplies that state through its tool shims instead.
func ComposeEvalPrompt(in EvalPromptInput) (EvalPrompt, error) {
	cfg := in.Config
	if cfg == nil {
		cfg = &config.Config{}
	}
	profiles := cfg.Agent.Profiles
	profile := profiles[in.ProfileName]
	backend := cmp.Or(in.Backend, "claude")
	started := in.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	runTimestamp := handoffRunTimestamp(started)
	runHandoffRelPath := handoffPathFor(runTimestamp, in.ProfileName)
	evidencePath, evidenceBlock := "", ""
	if len(profile.RequireEvidence) > 0 {
		evidencePath = evidenceRelPathFor(in.ProfileName)
		evidenceBlock = buildEvidenceBlock(profile.RequireEvidence, evidencePath)
	}
	runVars := runBindings(runTimestamp, runHandoffRelPath, cfg.Workspace.BaseBranch, evidencePath, nil)
	ruleSet := resolveRulesFor(cfg, in.ProfileName, backend)
	runVars["rules"] = ruleSet.Binding()

	rendered, err := prompt.RenderWith(cfg.PromptTemplate, in.Issue, nil, runVars)
	if err != nil {
		return EvalPrompt{}, err
	}
	rendered += "\n\n" + operatorReplyEnvelope
	rendered += "\n\n" + buildRunContextBlock(runTimestamp, runHandoffRelPath)
	if evidenceBlock != "" {
		rendered += "\n\n" + evidenceBlock
	}
	moveState := ""
	if in.Automation != nil && in.Automation.Trigger.Type == config.AutomationTriggerBlockersResolved {
		moveState = strings.TrimSpace(in.Automation.MoveToState)
	}
	actions := filterAllowedActionsForAutomation(profile.AllowedActions, in.Automation)
	rendered += profilePromptTail(profileTailInput{
		Rules:         ruleSet,
		Template:      cfg.PromptTemplate,
		ProfileName:   in.ProfileName,
		Profiles:      profiles,
		Automation:    in.Automation,
		Issue:         in.Issue,
		RunVars:       runVars,
		ActionContext: buildAgentActionContext(actions, strings.TrimSpace(profile.CreateIssueState), moveState, false),
		Backend:       backend,
	})
	return EvalPrompt{Text: rendered, Rules: ruleSet}, nil
}
