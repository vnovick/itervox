package skills

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// scanClaudeSkills discovers SKILL.md files under <projectDir>/.claude/skills
// and <homeDir>/.claude/skills. Returns a deduplicated slice keyed by
// (Name, Source). Malformed YAML frontmatter is skipped with an slog.Warn
// rather than aborting the whole scan — a single bad skill should not block
// inventory.
//
// homeDir == "" disables the user-home walk.
func scanClaudeSkills(projectDir, homeDir string) ([]Skill, error) {
	var out []Skill
	seen := make(map[string]struct{}, 16)

	add := func(s Skill) {
		key := s.Name + "|" + s.Source
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}

	if projectDir != "" {
		walked, err := walkClaudeSkills(filepath.Join(projectDir, ".claude", "skills"), "project")
		if err != nil {
			return nil, err
		}
		for _, s := range walked {
			add(s)
		}
	}
	if homeDir != "" {
		walked, err := walkClaudeSkills(filepath.Join(homeDir, ".claude", "skills"), "user")
		if err != nil {
			return nil, err
		}
		for _, s := range walked {
			add(s)
		}
	}
	return out, nil
}

// walkClaudeSkills walks one .claude/skills root and returns the parsed
// skills. A missing root returns (nil, nil) — typical for repos without
// project skills or for users without ~/.claude/skills.
func walkClaudeSkills(root, source string) ([]Skill, error) {
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}

	var out []Skill
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Surface but don't abort — a permission error on one subdir
			// shouldn't shut down the whole inventory.
			slog.Warn("skills: walk error", "path", path, "err", err)
			return nil
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		s, ok := parseSkillFile(path, source)
		if !ok {
			return nil
		}
		out = append(out, s)
		return nil
	})
	return out, walkErr
}

// skillFrontmatter is the subset of YAML frontmatter we read.
type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Trigger     string `yaml:"trigger"`
}

// parseSkillFile reads one SKILL.md and returns a Skill. Malformed files are
// reported via slog.Warn and produce ok=false.
func parseSkillFile(path, source string) (Skill, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("skills: cannot read SKILL.md", "path", path, "err", err)
		return Skill{}, false
	}
	front, skillBody, ok := splitFrontmatter(body)
	if !ok {
		slog.Warn("skills: missing frontmatter", "path", path)
		return Skill{}, false
	}
	var fm skillFrontmatter
	if err := yaml.Unmarshal(front, &fm); err != nil {
		slog.Warn("skills: malformed yaml frontmatter", "path", path, "err", err)
		return Skill{}, false
	}
	if fm.Name == "" {
		slog.Warn("skills: SKILL.md missing required 'name' field", "path", path)
		return Skill{}, false
	}
	var triggers []string
	if fm.Trigger != "" {
		triggers = []string{fm.Trigger}
	}
	return Skill{
		Name:            fm.Name,
		Description:     fm.Description,
		Provider:        "claude",
		Source:          source,
		FilePath:        path,
		ApproxTokens:    len(body) / 4,
		TriggerPatterns: triggers,
		bodyText:        string(skillBody),
	}, true
}

// splitFrontmatter extracts the YAML block between leading `---\n` and
// closing `\n---\n` markers. Returns frontmatter, body, ok.
func splitFrontmatter(content []byte) (front []byte, body []byte, ok bool) {
	s := string(content)
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return nil, nil, false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(s, "---\r\n"), "---\n")
	frontStr, bodyStr, found := strings.Cut(rest, "\n---")
	if !found {
		return nil, nil, false
	}
	front = []byte(frontStr)
	body = []byte(strings.TrimPrefix(strings.TrimPrefix(bodyStr, "\r\n"), "\n"))
	return front, body, true
}
