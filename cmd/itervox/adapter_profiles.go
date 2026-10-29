package main

import (
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/automationconfig"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/workflow"
)

func (a *orchestratorAdapter) ProfileDefs() map[string]server.ProfileDef {
	profiles := a.orch.ProfilesCfg()
	defs := make(map[string]server.ProfileDef, len(profiles))
	for name, p := range profiles {
		defs[name] = profileDefFromConfig(p)
	}
	return defs
}

// DefaultAgentCommand returns agent.command, read-only after startup (not a
// cfgMu field), for the dashboard's automation validation (CORE-010).
func (a *orchestratorAdapter) DefaultAgentCommand() string { return a.cfg.Agent.Command }

func (a *orchestratorAdapter) UpsertProfile(name string, def server.ProfileDef, originalName string) error {
	unlock, lockErr := a.beginSettingsSave()
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	currentProfiles := a.orch.ProfilesCfg()
	if currentProfiles == nil {
		currentProfiles = make(map[string]config.AgentProfile)
	}
	renamingProfile := originalName != "" && originalName != name
	if originalName != "" && originalName != name {
		if _, exists := currentProfiles[name]; exists {
			return fmt.Errorf("profile %q already exists", name)
		}
	} else if _, exists := currentProfiles[name]; exists && originalName == "" {
		return fmt.Errorf("profile %q already exists", name)
	}
	finalProfiles := make(map[string]config.AgentProfile, len(currentProfiles)+1)
	maps.Copy(finalProfiles, currentProfiles)
	if originalName != "" && originalName != name {
		delete(finalProfiles, originalName)
	}
	existingProfile := config.AgentProfile{}
	if originalName != "" {
		existingProfile = currentProfiles[originalName]
	} else {
		existingProfile = currentProfiles[name]
	}
	soulFile := strings.TrimSpace(def.SoulFile)
	instructionsFile := strings.TrimSpace(def.InstructionsFile)
	if originalName == "" || originalName == name {
		if soulFile == "" {
			soulFile = existingProfile.SoulFile
		}
		if instructionsFile == "" {
			instructionsFile = existingProfile.InstructionsFile
		}
	}
	soul := existingProfile.Soul
	if originalName == "" || def.SoulSet {
		soul = def.Soul
	}
	instructions := existingProfile.Instructions
	if originalName == "" || def.InstructionsSet {
		instructions = def.Instructions
	}
	if !def.InstructionsSet && instructions == "" {
		instructions = def.Prompt
	}
	nextProfile := config.AgentProfile{
		Command:          strings.TrimSpace(def.Command),
		Prompt:           def.Prompt,
		SoulFile:         soulFile,
		InstructionsFile: instructionsFile,
		Soul:             soul,
		Instructions:     instructions,
		Backend:          def.Backend,
		Enabled:          func() *bool { enabled := def.Enabled; return &enabled }(),
		AllowedActions:   config.NormalizeAllowedActions(def.AllowedActions),
		CreateIssueState: strings.TrimSpace(def.CreateIssueState),
		// Not edited by the dashboard: carried over from the saved profile
		// so a dashboard save does not strip them from WORKFLOW.md.
		PermissionMode:  existingProfile.PermissionMode,
		RequireEvidence: existingProfile.RequireEvidence,
		// #258: likewise the sandbox settings.
		Isolation:           existingProfile.Isolation,
		IsolationExtraWrite: existingProfile.IsolationExtraWrite,
		IsolationExtraDeny:  existingProfile.IsolationExtraDeny,
	}
	if a.cfg != nil && a.cfg.SchemaVersion >= config.LatestWorkflowSchemaVersion {
		if strings.TrimSpace(nextProfile.Instructions) == "" && strings.TrimSpace(def.Prompt) != "" {
			nextProfile.Instructions = def.Prompt
		}
		nextProfile.Prompt = ""
		var err error
		nextProfile, err = ensureSchema2ProfileFiles(
			a.workflowPath,
			name,
			nextProfile,
			originalName == "" && !def.SoulSet,
			originalName == "" && !def.InstructionsSet && strings.TrimSpace(def.Prompt) == "",
		)
		if err != nil {
			return err
		}
	}
	finalProfiles[name] = nextProfile
	automations := a.orch.AutomationsCfg()
	automationsChanged := false
	if renamingProfile {
		var renamed bool
		automations, renamed = renameAutomationsProfile(automations, originalName, name)
		automationsChanged = automationsChanged || renamed
	}
	if !def.Enabled {
		var disabled bool
		automations, disabled = disableAutomationsForProfile(automations, name)
		automationsChanged = automationsChanged || disabled
	}
	reviewerProfile, autoReview := a.orch.ReviewerCfg()
	reviewerChanged := false
	if renamingProfile && reviewerProfile == originalName {
		reviewerProfile = name
		reviewerChanged = true
	}
	if !def.Enabled && reviewerProfile == name {
		reviewerProfile = ""
		autoReview = false
		reviewerChanged = true
	}

	// Persist profiles + automations + reviewer config in ONE save: a single
	// atomic rewrite of WORKFLOW.md (the profiles block), after the settings
	// file write it is transactional with (persistSettings, #239). Previously this issued up to four sequential
	// writes; a SIGKILL or atomicfs failure between them could leave the file
	// referencing a renamed profile that the profiles block had not yet been
	// updated to declare. ApplyAndWriteFrontMatter composes mutators against one
	// in-memory copy of frontLines and writes once.
	mutators := []workflow.Mutator{
		workflow.MutateProfilesBlock(profilesToEntries(finalProfiles)),
	}
	var saved []setting
	if automationsChanged {
		// #239: automations live in the settings file; they ride in the
		// same save as the profiles block.
		saved = append(saved, setting{Key: "automations", Value: automationsSetting(automationconfig.DefinitionsFromConfigs(automations))})
	}
	if err := persistProfileSave(a.workflowPath, mutators, saved, reviewerChanged, reviewerProfile, autoReview); err != nil {
		return err
	}
	a.orch.SetProfilesCfg(finalProfiles)
	if automationsChanged {
		a.orch.SetAutomationsCfg(automations)
	}
	if reviewerChanged {
		if err := a.orch.SetReviewerCfg(reviewerProfile, autoReview); err != nil {
			slog.Error("settings: reviewer config rejected after WORKFLOW.md was written; "+
				"WORKFLOW.md and the running config disagree until the next reload",
				"reviewer_profile", reviewerProfile, "error", err)
			return err
		}
	}
	a.notify()
	return nil
}

func ensureSchema2ProfileFiles(workflowPath, name string, profile config.AgentProfile, useStarterSoul, useStarterInstructions bool) (config.AgentProfile, error) {
	baseDir := filepath.Dir(workflowPath)
	if baseDir == "" {
		baseDir = "."
	}
	if strings.TrimSpace(profile.SoulFile) == "" {
		profile.SoulFile = filepath.ToSlash(filepath.Join(".itervox", "agents", name, "SOUL.md"))
	}
	if strings.TrimSpace(profile.InstructionsFile) == "" {
		profile.InstructionsFile = filepath.ToSlash(filepath.Join(".itervox", "agents", name, "INSTRUCTIONS.md"))
	}

	soulPath := resolveProfileFilePath(baseDir, profile.SoulFile)
	instructionsPath := resolveProfileFilePath(baseDir, profile.InstructionsFile)
	if err := os.MkdirAll(filepath.Dir(soulPath), 0o755); err != nil {
		return profile, fmt.Errorf("profile %q: create profile files: %w", name, err)
	}
	if err := os.MkdirAll(filepath.Dir(instructionsPath), 0o755); err != nil {
		return profile, fmt.Errorf("profile %q: create profile files: %w", name, err)
	}
	soul := profile.Soul
	if useStarterSoul && strings.TrimSpace(soul) == "" {
		soul = initSoulContent(name)
	}
	instructions := profile.Instructions
	if useStarterInstructions && strings.TrimSpace(instructions) == "" {
		instructions = initInstructionsContent(name, profile.Command)
	}
	if err := atomicfs.WriteFile(soulPath, []byte(soul), 0o644); err != nil {
		return profile, fmt.Errorf("profile %q: write %s: %w", name, soulPath, err)
	}
	if err := atomicfs.WriteFile(instructionsPath, []byte(instructions), 0o644); err != nil {
		return profile, fmt.Errorf("profile %q: write %s: %w", name, instructionsPath, err)
	}
	profile.Soul = soul
	profile.Instructions = instructions
	return profile, nil
}

func resolveProfileFilePath(baseDir, configuredPath string) string {
	cleaned := filepath.FromSlash(strings.TrimSpace(configuredPath))
	if filepath.IsAbs(cleaned) {
		return cleaned
	}
	return filepath.Join(baseDir, cleaned)
}

func profileDefFromConfig(p config.AgentProfile) server.ProfileDef {
	prompt := p.Prompt
	if strings.TrimSpace(p.Instructions) != "" {
		prompt = p.Instructions
	}
	return server.ProfileDef{
		Command:          p.Command,
		Prompt:           prompt,
		Soul:             p.Soul,
		Instructions:     p.Instructions,
		SoulFile:         p.SoulFile,
		InstructionsFile: p.InstructionsFile,
		Backend:          p.Backend,
		Enabled:          config.ProfileEnabled(p),
		AllowedActions:   config.NormalizeAllowedActions(p.AllowedActions),
		CreateIssueState: p.CreateIssueState,
	}
}

func (a *orchestratorAdapter) DeleteProfile(name string) error {
	unlock, lockErr := a.beginSettingsSave()
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	profiles := a.orch.ProfilesCfg()
	delete(profiles, name)
	automations, automationsChanged := removeAutomationsForProfile(a.orch.AutomationsCfg(), name)
	reviewerProfile, autoReview := a.orch.ReviewerCfg()
	reviewerChanged := false
	if reviewerProfile == name {
		reviewerProfile = ""
		autoReview = false
		reviewerChanged = true
	}
	// Single atomic rewrite — see comment in UpsertProfile above for the
	// transactional rationale.
	mutators := []workflow.Mutator{
		workflow.MutateProfilesBlock(profilesToEntries(profiles)),
	}
	var saved []setting
	if automationsChanged {
		// #239: automations live in the settings file; they ride in the
		// same save as the profiles block.
		saved = append(saved, setting{Key: "automations", Value: automationsSetting(automationconfig.DefinitionsFromConfigs(automations))})
	}
	if err := persistProfileSave(a.workflowPath, mutators, saved, reviewerChanged, reviewerProfile, autoReview); err != nil {
		return err
	}
	a.orch.SetProfilesCfg(profiles)
	if automationsChanged {
		a.orch.SetAutomationsCfg(automations)
	}
	if reviewerChanged {
		if err := a.orch.SetReviewerCfg(reviewerProfile, autoReview); err != nil {
			slog.Error("settings: reviewer config rejected after WORKFLOW.md was written; "+
				"WORKFLOW.md and the running config disagree until the next reload",
				"reviewer_profile", reviewerProfile, "error", err)
			return err
		}
	}
	a.notify()
	return nil
}

// persistProfileSave writes a profile save: the profiles block stays in
// WORKFLOW.md (the profile editor's home), and the settings the save also
// changes — the automations it renamed or disabled (#239) and the reviewer
// it cleared or renamed — go to the settings file through persistSettings,
// with the profiles rewrite riding in persistSettings' one WORKFLOW.md write.
// Writing the reviewer into WORKFLOW.md instead (MutateReviewerConfig removes
// the keys to clear them) would let a reviewer the dashboard saved to the
// settings file show through again at the next load, naming a profile that
// no longer exists.
func persistProfileSave(path string, mutators []workflow.Mutator, saved []setting, reviewerChanged bool, reviewerProfile string, autoReview bool) error {
	if reviewerChanged {
		saved = append(saved, reviewerSettings(reviewerProfile, autoReview)...)
	}
	if len(saved) > 0 {
		return persistSettings(path, saved, mutators...)
	}
	return workflow.ApplyAndWriteFrontMatter(path, mutators...)
}
