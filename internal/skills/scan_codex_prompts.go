package skills

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// codexPromptFrontmatter is the subset of a custom prompt's frontmatter the
// inventory reads.
type codexPromptFrontmatter struct {
	Description string `yaml:"description"`
}

// scanCodexPrompts lists the custom prompts under <home>/.codex/prompts/.
// Only top-level *.md files count (Codex does not recurse). It returns the
// directory it looked at so the cache can watch it even before it exists.
// A missing directory is not an error.
func scanCodexPrompts(homeDir string) ([]Prompt, string, error) {
	if homeDir == "" {
		return nil, "", nil
	}
	dir := filepath.Join(homeDir, ".codex", "prompts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, dir, nil
		}
		return nil, dir, err
	}
	var out []Prompt
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("skills: cannot read codex prompt", "path", path, "err", err)
			continue
		}
		p := Prompt{
			Name:         strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())),
			Provider:     "codex",
			Source:       "user-codex",
			FilePath:     path,
			ApproxTokens: len(body) / 4,
		}
		if front, _, ok := splitFrontmatter(body); ok {
			var fm codexPromptFrontmatter
			if err := yaml.Unmarshal(front, &fm); err != nil {
				slog.Warn("skills: malformed codex prompt frontmatter", "path", path, "err", err)
			} else {
				p.Description = strings.TrimSpace(fm.Description)
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, dir, nil
}
