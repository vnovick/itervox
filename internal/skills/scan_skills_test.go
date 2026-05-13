package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, ".claude", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

const wellFormed = `---
name: my-skill
description: Use when X happens
trigger: /myskill
---

# My skill

Body of the skill.
`

const malformed = `---
name: not-balanced-yaml
description: [unclosed
---

body
`

const noFrontmatter = `# No frontmatter

Just body.
`

const noName = `---
description: no name field
---

body
`

func TestScanClaudeSkills_ProjectOnly(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeSkill(t, proj, "my-skill", wellFormed)

	skills, err := scanClaudeSkills(proj, "")
	if err != nil {
		t.Fatalf("scanClaudeSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d: %#v", len(skills), skills)
	}
	s := skills[0]
	if s.Name != "my-skill" || s.Provider != "claude" || s.Source != "project" {
		t.Errorf("unexpected skill fields: %+v", s)
	}
	if len(s.TriggerPatterns) != 1 || s.TriggerPatterns[0] != "/myskill" {
		t.Errorf("unexpected trigger patterns: %v", s.TriggerPatterns)
	}
	if s.ApproxTokens != len(wellFormed)/4 {
		t.Errorf("unexpected ApproxTokens %d", s.ApproxTokens)
	}
	if !strings.Contains(s.bodyText, "Body of the skill") {
		t.Errorf("expected skill body text to be retained for analysis")
	}
}

func TestScanClaudeSkills_UserOnly(t *testing.T) {
	t.Parallel()
	user := t.TempDir()
	writeSkill(t, user, "user-skill", wellFormed)

	skills, err := scanClaudeSkills("", user)
	if err != nil {
		t.Fatalf("scanClaudeSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Source != "user" {
		t.Errorf("expected Source=user, got %s", skills[0].Source)
	}
}

func TestScanClaudeSkills_ProjectAndUser(t *testing.T) {
	t.Parallel()
	proj, user := t.TempDir(), t.TempDir()
	writeSkill(t, proj, "p-skill", wellFormed)
	writeSkill(t, user, "u-skill", wellFormed)

	skills, err := scanClaudeSkills(proj, user)
	if err != nil {
		t.Fatalf("scanClaudeSkills: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Source < skills[j].Source })
	if skills[0].Source != "project" || skills[1].Source != "user" {
		t.Errorf("expected project then user, got %v", skills)
	}
}

func TestScanClaudeSkills_MalformedYamlSkipped(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeSkill(t, proj, "good", wellFormed)
	writeSkill(t, proj, "bad-yaml", malformed)
	writeSkill(t, proj, "no-fm", noFrontmatter)
	writeSkill(t, proj, "no-name", noName)

	skills, err := scanClaudeSkills(proj, "")
	if err != nil {
		t.Fatalf("scanClaudeSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected only the well-formed skill, got %d: %v", len(skills), skills)
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("expected my-skill, got %s", skills[0].Name)
	}
}

func TestScanClaudeSkills_EmptyDirReturnsNilNil(t *testing.T) {
	t.Parallel()
	skills, err := scanClaudeSkills(t.TempDir(), "")
	if err != nil {
		t.Fatalf("expected no error on empty dir, got %v", err)
	}
	if skills != nil {
		t.Errorf("expected nil slice on empty dir, got %v", skills)
	}
}

func TestScanClaudeSkills_DedupesByNameAndSource(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	// Two SKILL.md files in different subdirs but with the same `name:` —
	// dedup picks the first encountered. Filesystem walk order is stable
	// per-directory, so this test proves the dedup logic, not the
	// walk-order contract.
	writeSkill(t, proj, "a-dup", wellFormed)
	writeSkill(t, proj, "b-dup", wellFormed)

	skills, err := scanClaudeSkills(proj, "")
	if err != nil {
		t.Fatalf("scanClaudeSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected dedup to leave 1 skill, got %d", len(skills))
	}
}
