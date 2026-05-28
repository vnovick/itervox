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
	if version == config.LatestWorkflowSchemaVersion && !inlinePromptSeen {
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
	if err := ensureItervoxGitignore(filepath.Join(filepath.Dir(workflowPath), ".itervox")); err != nil {
		return result, err
	}
	if err := patchRootGitignoreForAgents(filepath.Dir(workflowPath)); err != nil {
		return result, err
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
	if force || fileMissing(soulPath) {
		if err := atomicfs.WriteFile(soulPath, []byte(migratedSoulContent(name, profile, promptText, now)), 0o644); err != nil {
			return fmt.Errorf("itervox init --update: write %s: %w", soulPath, err)
		}
	}
	if force || fileMissing(instructionsPath) {
		if err := atomicfs.WriteFile(instructionsPath, []byte(migratedInstructionsContent(name, promptText, now)), 0o644); err != nil {
			return fmt.Errorf("itervox init --update: write %s: %w", instructionsPath, err)
		}
	}
	return nil
}

func migratedSoulContent(name string, profile map[string]any, promptText string, now time.Time) string {
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
