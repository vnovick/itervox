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

// scanClaudeAgents discovers Claude Code subagent definitions (#86): every
// `*.md` file under <projectDir>/.claude/agents and <homeDir>/.claude/agents
// (subdirectories included) whose YAML frontmatter has a `name`. Plugin
// agents are added by subagentsFromPlugins. Malformed files are skipped with
// an slog.Warn, as for skills. homeDir == "" disables the user-home walk.
//
// watch lists the directories whose mtime should gate a rescan: each agents
// root (present or not) and every directory walked beneath it.
func scanClaudeAgents(projectDir, homeDir string) (out []Subagent, watch []string, err error) {
	seen := make(map[string]struct{}, 8)
	add := func(a Subagent) {
		key := a.Name + "|" + a.Source
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, a)
	}
	var errs []error
	for _, root := range []struct{ dir, source string }{
		{projectDir, "project"},
		{homeDir, "user"},
	} {
		if root.dir == "" {
			continue
		}
		agentsRoot := filepath.Join(root.dir, ".claude", "agents")
		watch = append(watch, agentsRoot)
		walked, dirs, err := walkClaudeAgents(agentsRoot, root.source)
		watch = append(watch, dirs...)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, a := range walked {
			add(a)
		}
	}
	return out, watch, errors.Join(errs...)
}

// walkClaudeAgents walks one .claude/agents root and returns its agents and
// every directory it walked. A missing root returns (nil, nil, nil).
func walkClaudeAgents(root, source string) (out []Subagent, dirs []string, err error) {
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, nil
	}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("skills: agents walk error", "path", path, "err", err)
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		if a, ok := parseAgentFile(path, source); ok {
			out = append(out, a)
		}
		return nil
	})
	return out, dirs, walkErr
}

// agentFrontmatter is the subset of a subagent's frontmatter we read.
// `tools` is a comma-separated string in Claude Code's documented format; a
// YAML list is accepted too.
type agentFrontmatter struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description"`
	Tools       yaml.Node `yaml:"tools"`
	Model       string    `yaml:"model"`
}

func parseAgentFile(path, source string) (Subagent, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("skills: cannot read subagent file", "path", path, "err", err)
		return Subagent{}, false
	}
	front, _, ok := splitFrontmatter(body)
	if !ok {
		// README.md and other notes in the directory are not agents.
		return Subagent{}, false
	}
	var fm agentFrontmatter
	if err := yaml.Unmarshal(front, &fm); err != nil {
		slog.Warn("skills: malformed subagent frontmatter", "path", path, "err", err)
		return Subagent{}, false
	}
	if strings.TrimSpace(fm.Name) == "" {
		slog.Warn("skills: subagent file missing required 'name' field", "path", path)
		return Subagent{}, false
	}
	return Subagent{
		Name:         strings.TrimSpace(fm.Name),
		Description:  fm.Description,
		Tools:        parseAgentTools(fm.Tools),
		Model:        strings.TrimSpace(fm.Model),
		Provider:     "claude",
		Source:       source,
		FilePath:     path,
		ApproxTokens: len(body) / 4,
	}, true
}

// parseAgentTools accepts `tools: Read, Grep, Bash` or a YAML list.
func parseAgentTools(n yaml.Node) []string {
	var raw []string
	switch n.Kind {
	case yaml.ScalarNode:
		raw = strings.Split(n.Value, ",")
	case yaml.SequenceNode:
		for _, c := range n.Content {
			raw = append(raw, c.Value)
		}
	}
	var out []string
	for _, t := range raw {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// subagentsFromPlugins lifts the agents each plugin manifest declares into
// Subagent entries with Source "plugin:<name>". The agent file, when it
// exists, supplies description, tools, model and size.
func subagentsFromPlugins(plugins []Plugin) []Subagent {
	var out []Subagent
	for _, p := range plugins {
		if p.Provider != "" && p.Provider != "claude" {
			continue
		}
		for _, a := range p.Agents {
			sa := Subagent{
				Name:        a.Name,
				Description: a.Description,
				Provider:    "claude",
				Source:      "plugin:" + p.Name,
				FilePath:    a.FilePath,
			}
			if a.FilePath != "" {
				if parsed, ok := parseAgentFile(a.FilePath, sa.Source); ok {
					sa.Tools, sa.Model, sa.ApproxTokens = parsed.Tools, parsed.Model, parsed.ApproxTokens
					if sa.Description == "" {
						sa.Description = parsed.Description
					}
				}
			}
			out = append(out, sa)
		}
	}
	return out
}
