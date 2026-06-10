package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	builtinprofiles "github.com/vnovick/itervox/internal/profiles"
	"gopkg.in/yaml.v3"
)

type workflowMigrationResult struct {
	Changed    bool
	BackupPath string
	Profiles   []string
	Warnings   []string
}

func migrateWorkflowToSchema2(workflowPath string, force bool, now time.Time) (workflowMigrationResult, error) {
	var result workflowMigrationResult
	original, err := os.ReadFile(workflowPath)
	if err != nil {
		return result, fmt.Errorf("itervox init --update: read %s: %w", workflowPath, err)
	}
	front, body, ok := splitWorkflowFrontMatter(string(original))
	if !ok {
		return result, fmt.Errorf("itervox init --update: %s has no YAML front matter", workflowPath)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		return result, fmt.Errorf("itervox init --update: parse %s: %w", workflowPath, err)
	}
	if doc == nil {
		doc = make(map[string]any)
	}
	version := yamlInt(doc["itervox_schema_version"])
	if version > config.LatestWorkflowSchemaVersion {
		return result, fmt.Errorf("itervox init --update: unsupported itervox_schema_version %d", version)
	}

	if workspace := yamlMap(doc["workspace"]); workspace != nil {
		if v, ok := workspace["auto_clear"]; ok {
			if b, isBool := v.(bool); isBool && b {
				result.Warnings = append(result.Warnings,
					"workspace.auto_clear: true semantics changed in v0.2.0 — workspace is now cleared only when the issue reaches a terminal tracker state (success or final failure after retries), not after every successful run. See CHANGELOG v0.2.0.")
			}
		}
	}

	agent := yamlMap(doc["agent"])
	profiles := yamlMap(agent["profiles"])
	names := sortedProfileNames(profiles)
	inlinePromptSeen := false
	for _, name := range names {
		profile := yamlMap(profiles[name])
		if _, ok := profile["prompt"]; ok {
			inlinePromptSeen = true
			break
		}
	}
	// v0.2.0 todolist5 — `agent.reviewer_prompt` is the legacy top-level
	// reviewer template that pre-dates file-backed profiles. When the
	// schema marker is already 2, the previous idempotency check missed it
	// and reported "already uses schema 2" while leaving the legacy block
	// in place. Now we detect it and (with --force) move its content into
	// the reviewer profile's INSTRUCTIONS.md.
	reviewerProfileName := yamlString(agent["reviewer_profile"])
	legacyReviewerPrompt := strings.TrimSpace(yamlString(agent["reviewer_prompt"]))
	legacyReviewerPromptSeen := legacyReviewerPrompt != "" &&
		legacyReviewerPrompt != strings.TrimSpace(config.DefaultReviewerPrompt)

	// Detect the dependency-analyzer scaffold gap: workflows generated before
	// the deps-analyzer profile + agent.deps_analyzer_profile field landed
	// need both written. Setting the field alone (without the profile) would
	// fail validation, so we always pair the two.
	depsAnalyzerFieldUnset := strings.TrimSpace(yamlString(agent["deps_analyzer_profile"])) == ""
	depsAnalyzerProfileMissing := yamlMap(profiles[initDepsAnalyzerProfileName]) == nil
	needsDepsAnalyzerScaffold := depsAnalyzerFieldUnset || depsAnalyzerProfileMissing

	// We only rewrite WORKFLOW.md when there is migratable content:
	//   - inline `prompt:` inside any profile (always migratable), or
	//   - legacy `reviewer_prompt` AND the user passed --force, or
	//   - the deps-analyzer scaffold is missing (field unset or profile entry missing).
	// Without --force the legacy reviewer_prompt yields a warning only, so
	// the caller can decide whether to re-run with --force after reading the
	// reviewer profile's INSTRUCTIONS.md.
	needsRewrite := inlinePromptSeen || (legacyReviewerPromptSeen && force) || needsDepsAnalyzerScaffold
	if version == config.LatestWorkflowSchemaVersion && !needsRewrite {
		if legacyReviewerPromptSeen {
			result.Warnings = append(result.Warnings,
				"agent.reviewer_prompt is a legacy field still consumed by worker.go (base Liquid template for reviewer runs). Re-run with --force to append it to the reviewer profile's INSTRUCTIONS.md and delete the inline field. NOTE: with --force the migrated content becomes an appended profile block instead of the base template — placeholders still resolve identically, but the prompt ordering changes (base falls back to DefaultReviewerPrompt). Review the appended section before the next reviewer run.")
		}
		if err := patchRootGitignoreForAgents(filepath.Dir(workflowPath)); err != nil {
			return result, err
		}
		return result, nil
	}

	if !force {
		for _, name := range names {
			profile := yamlMap(profiles[name])
			if profile == nil {
				continue
			}
			if _, hasPrompt := profile["prompt"]; !hasPrompt {
				continue
			}
			for _, path := range migratedAgentFilePaths(workflowPath, name) {
				if !fileMissing(path) {
					return result, fmt.Errorf("itervox init --update: %s already exists; rerun with --force to overwrite generated agent files or move the file before migrating", path)
				}
			}
		}
	}

	backupPath := workflowPath + ".bak"
	// todolist4 A.3: refuse to silently overwrite a stale .bak without --force.
	if !force && !fileMissing(backupPath) {
		return result, fmt.Errorf("itervox init --update: %s already exists; rerun with --force to overwrite or move/delete the existing backup first", backupPath)
	}
	if err := atomicfs.WriteFile(backupPath, original, 0o644); err != nil {
		return result, fmt.Errorf("itervox init --update: write %s: %w", backupPath, err)
	}
	result.BackupPath = backupPath

	doc["itervox_schema_version"] = config.LatestWorkflowSchemaVersion
	if agent == nil {
		agent = make(map[string]any)
		doc["agent"] = agent
	}
	if profiles != nil {
		for _, name := range names {
			profile := yamlMap(profiles[name])
			if profile == nil {
				continue
			}
			promptText := yamlString(profile["prompt"])
			delete(profile, "prompt")
			soulRel := filepath.ToSlash(filepath.Join(".itervox", "agents", name, "SOUL.md"))
			instructionsRel := filepath.ToSlash(filepath.Join(".itervox", "agents", name, "INSTRUCTIONS.md"))
			profile["soul_file"] = soulRel
			profile["instructions_file"] = instructionsRel
			if err := writeMigratedAgentFiles(workflowPath, name, profile, promptText, force, now); err != nil {
				return result, err
			}
			result.Profiles = append(result.Profiles, name)
		}
	}
	if needsDepsAnalyzerScaffold {
		if profiles == nil {
			profiles = make(map[string]any)
			agent["profiles"] = profiles
		}
		runner := detectMigrationRunner(profiles)
		if depsAnalyzerProfileMissing {
			command, backend := initProfileCommand(runner)
			depsEntry := map[string]any{
				"command":           command,
				"soul_file":         filepath.ToSlash(filepath.Join(".itervox", "agents", initDepsAnalyzerProfileName, "SOUL.md")),
				"instructions_file": filepath.ToSlash(filepath.Join(".itervox", "agents", initDepsAnalyzerProfileName, "INSTRUCTIONS.md")),
			}
			if backend != "" {
				depsEntry["backend"] = backend
			}
			profiles[initDepsAnalyzerProfileName] = depsEntry
			if err := writeDepsAnalyzerProfileFiles(workflowPath, runner); err != nil {
				return result, err
			}
			result.Profiles = append(result.Profiles, initDepsAnalyzerProfileName)
		}
		if depsAnalyzerFieldUnset {
			agent["deps_analyzer_profile"] = initDepsAnalyzerProfileName
		}
	}
	// v0.2.0 todolist5 — legacy `agent.reviewer_prompt` migration. Append the
	// template into the reviewer profile's INSTRUCTIONS.md under a clearly
	// labelled section, then drop the inline field. Only runs with --force
	// because the appended content changes the reviewer profile's effective
	// prompt envelope (existing INSTRUCTIONS.md content is preserved).
	if legacyReviewerPromptSeen && force {
		if err := appendLegacyReviewerPrompt(workflowPath, reviewerProfileName, legacyReviewerPrompt, now); err != nil {
			return result, err
		}
		delete(agent, "reviewer_prompt")
		result.Warnings = append(result.Warnings,
			"agent.reviewer_prompt migrated into the reviewer profile's INSTRUCTIONS.md and removed from WORKFLOW.md. Review the appended section before the next reviewer run.")
	}
	if err := ensureItervoxGitignore(filepath.Join(filepath.Dir(workflowPath), ".itervox")); err != nil {
		return result, err
	}
	if err := patchRootGitignoreForAgents(filepath.Dir(workflowPath)); err != nil {
		return result, err
	}
	// P0-A — scaffold built-in profile files for any built-in name that the
	// WORKFLOW.md profile map references. Idempotent (writeFileIfMissing).
	// IsBuiltin pre-filters the iteration so non-built-in profile names
	// (operator-authored or migrated-from-legacy) skip the built-in scaffold
	// path entirely.
	if profiles != nil {
		referenced := make([]string, 0, len(profiles))
		for name := range profiles {
			if builtinprofiles.IsBuiltin(name) {
				referenced = append(referenced, name)
			}
		}
		if len(referenced) > 0 {
			if err := writeBuiltinProfileFilesIfMissing(workflowPath, referenced); err != nil {
				return result, err
			}
		}
	}

	encoded, err := yaml.Marshal(doc)
	if err != nil {
		return result, fmt.Errorf("itervox init --update: marshal %s: %w", workflowPath, err)
	}
	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(encoded)
	out.WriteString("---\n")
	out.WriteString(body)
	if body != "" && !strings.HasSuffix(body, "\n") {
		out.WriteByte('\n')
	}
	if err := atomicfs.WriteFile(workflowPath, out.Bytes(), 0o644); err != nil {
		return result, fmt.Errorf("itervox init --update: write %s: %w", workflowPath, err)
	}
	result.Changed = true
	return result, nil
}

func splitWorkflowFrontMatter(content string) (front string, body string, ok bool) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return "", normalized, false
	}
	rest := normalized[len("---\n"):]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return rest, "", true
	}
	front = rest[:idx]
	body = strings.TrimPrefix(rest[idx+len("\n---"):], "\n")
	return front, body, true
}

func sortedProfileNames(profiles map[string]any) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func yamlMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func yamlString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func yamlInt(v any) int {
	switch typed := v.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	}
	return 0
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

func migratedAgentFilePaths(workflowPath, name string) []string {
	dir := filepath.Join(filepath.Dir(workflowPath), ".itervox", "agents", name)
	return []string{
		filepath.Join(dir, "SOUL.md"),
		filepath.Join(dir, "INSTRUCTIONS.md"),
	}
}

func writeMigratedAgentFiles(workflowPath, name string, profile map[string]any, promptText string, force bool, now time.Time) error {
	dir := filepath.Join(filepath.Dir(workflowPath), ".itervox", "agents", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("itervox init --update: create %s: %w", dir, err)
	}
	soulPath := filepath.Join(dir, "SOUL.md")
	instructionsPath := filepath.Join(dir, "INSTRUCTIONS.md")
	// v0.2.0 todolist5 — when the profile carries no inline `prompt:` (the
	// already-migrated case), the SOUL/INSTRUCTIONS files are operator-owned
	// and must not be overwritten — even with --force. --force is reserved
	// for replacing the boilerplate scaffolds, not user-authored content.
	hasInlinePrompt := strings.TrimSpace(promptText) != ""
	if shouldWriteAgentFile(soulPath, hasInlinePrompt, force) {
		if err := atomicfs.WriteFile(soulPath, []byte(migratedSoulContent(name, profile, promptText, now)), 0o644); err != nil {
			return fmt.Errorf("itervox init --update: write %s: %w", soulPath, err)
		}
	}
	if shouldWriteAgentFile(instructionsPath, hasInlinePrompt, force) {
		if err := atomicfs.WriteFile(instructionsPath, []byte(migratedInstructionsContent(name, promptText, now)), 0o644); err != nil {
			return fmt.Errorf("itervox init --update: write %s: %w", instructionsPath, err)
		}
	}
	return nil
}

// shouldWriteAgentFile returns true when the SOUL/INSTRUCTIONS file at `path`
// should be (over)written by the profile-migration step. Rules:
//   - File missing: always write — scaffolds need to exist for schema 2.
//   - Profile has an inline `prompt:` to migrate: write (overwrite when --force).
//   - Profile has no inline prompt AND the file already exists: leave alone.
//     This is the "already migrated, operator-edited" case — destroying that
//     content was the pre-fix bug.
func shouldWriteAgentFile(path string, hasInlinePrompt, force bool) bool {
	if fileMissing(path) {
		return true
	}
	return hasInlinePrompt && force
}

func migratedSoulContent(name string, profile map[string]any, promptText string, now time.Time) string {
	if bp := builtinprofiles.Lookup(name); bp != nil && strings.TrimSpace(promptText) == "" {
		return bp.Soul + "\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- Generated by itervox init --update from agent.profiles.%s on %s. -->\n\n", name, now.Format(time.RFC3339))
	fmt.Fprintf(&b, "# %s SOUL\n\n", name)
	b.WriteString("## Identity\n")
	if sentence := firstYouAreSentence(promptText); sentence != "" {
		b.WriteString(sentence)
		b.WriteString("\n\n")
	} else {
		fmt.Fprintf(&b, "You are the %s agent for this repository.\n\n", name)
	}
	b.WriteString("## Purpose\n")
	b.WriteString("Support the tracker issue using this profile's configured runner.\n\n")
	b.WriteString("## Boundaries\n")
	b.WriteString("Do not change unrelated files. Do not commit secrets.\n\n")
	b.WriteString("## Profile\n")
	if command := yamlString(profile["command"]); command != "" {
		fmt.Fprintf(&b, "- Command: `%s`\n", command)
	}
	if backend := yamlString(profile["backend"]); backend != "" {
		fmt.Fprintf(&b, "- Backend: `%s`\n", backend)
	}
	return b.String()
}

func migratedInstructionsContent(name string, promptText string, now time.Time) string {
	if bp := builtinprofiles.Lookup(name); bp != nil && strings.TrimSpace(promptText) == "" {
		return bp.Instructions + "\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- Generated by itervox init --update from agent.profiles.%s.prompt on %s. -->\n\n", name, now.Format(time.RFC3339))
	fmt.Fprintf(&b, "# %s INSTRUCTIONS\n\n", name)
	b.WriteString("## Migrated Instructions\n\n")
	if strings.TrimSpace(promptText) == "" {
		b.WriteString("TODO: replace this starter section with operational instructions for this profile.\n")
		return b.String()
	}
	b.WriteString(promptText)
	if !strings.HasSuffix(promptText, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

// appendLegacyReviewerPrompt writes the legacy `agent.reviewer_prompt`
// content into the reviewer profile's INSTRUCTIONS.md. If INSTRUCTIONS.md
// already exists it is APPENDED with a labelled section so existing content
// is preserved; if absent, a fresh file is created. v0.2.0 todolist5.
//
// `reviewerProfile` is the value of `agent.reviewer_profile` from the
// workflow front matter. Empty falls back to "reviewer" — the conventional
// scaffold name.
func appendLegacyReviewerPrompt(workflowPath, reviewerProfile, prompt string, now time.Time) error {
	if reviewerProfile == "" {
		reviewerProfile = "reviewer"
	}
	dir := filepath.Join(filepath.Dir(workflowPath), ".itervox", "agents", reviewerProfile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("itervox init --update: create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "INSTRUCTIONS.md")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("itervox init --update: read %s: %w", path, err)
	}
	var b strings.Builder
	if len(existing) > 0 {
		b.Write(existing)
		if !bytes.HasSuffix(existing, []byte("\n")) {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	} else {
		fmt.Fprintf(&b, "<!-- Created by itervox init --update on %s. -->\n\n# %s INSTRUCTIONS\n\n",
			now.Format(time.RFC3339), reviewerProfile)
	}
	fmt.Fprintf(&b, "## Reviewer Template (migrated from agent.reviewer_prompt on %s)\n\n",
		now.Format(time.RFC3339))
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n")
	if err := atomicfs.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("itervox init --update: write %s: %w", path, err)
	}
	return nil
}

var youAreSentenceRE = regexp.MustCompile(`(?is)\bYou are\b[^.!?\n]*(?:[.!?]|$)`)

func firstYouAreSentence(text string) string {
	match := youAreSentenceRE.FindString(text)
	return strings.TrimSpace(match)
}

func patchRootGitignoreForAgents(projectDir string) error {
	if projectDir == "" {
		projectDir = "."
	}
	path := filepath.Join(projectDir, ".gitignore")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("itervox init --update: read %s: %w", path, err)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !rootGitignoreHidesAgents(text) {
		return nil
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	needed := []string{
		"!.itervox/",
		"!.itervox/agents/",
		"!.itervox/agents/**",
		"!.itervox/handoff/",
		"!.itervox/handoff/**",
	}
	for _, line := range needed {
		if !containsLine(lines, line) {
			lines = append(lines, line)
		}
	}
	if err := atomicfs.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("itervox init --update: write %s: %w", path, err)
	}
	return nil
}

func rootGitignoreHidesAgents(text string) bool {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "/")
		if line == ".itervox" ||
			line == ".itervox/" ||
			line == ".itervox/*" ||
			line == ".itervox/**" ||
			line == ".itervox/agents" ||
			line == ".itervox/agents/" ||
			line == ".itervox/agents/*" ||
			line == ".itervox/agents/**" {
			return true
		}
	}
	return false
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}
