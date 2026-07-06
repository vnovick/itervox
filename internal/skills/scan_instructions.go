package skills

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// maxInstructionFiles caps the recursive walk for nested CLAUDE.md to prevent
// pathological projects (e.g. node_modules-style trees) from dominating the
// scan budget.
const maxInstructionFiles = 100

// scanInstructions discovers instruction docs across the project + user
// layouts:
//
//   - <projectDir>/CLAUDE.md  (provider: claude)
//   - <projectDir>/AGENTS.md  (provider: codex)
//   - <projectDir>/**/CLAUDE.md  (recursive — capped at 100 files)
//   - <homeDir>/.claude/CLAUDE.md  (user-scope claude)
//
// Symlinks are followed but de-duplicated by canonical (resolved) path.
func scanInstructions(projectDir, homeDir string) ([]InstructionDoc, error) {
	var out []InstructionDoc
	seenPaths := make(map[string]struct{})

	add := func(d InstructionDoc) {
		canonical, err := filepath.EvalSymlinks(d.FilePath)
		if err != nil {
			canonical = d.FilePath
		}
		if _, dup := seenPaths[canonical]; dup {
			return
		}
		seenPaths[canonical] = struct{}{}
		out = append(out, d)
	}

	if projectDir != "" {
		walked, err := walkProjectInstructions(projectDir)
		if err != nil {
			return nil, err
		}
		for _, d := range walked {
			add(d)
		}
	}
	if homeDir != "" {
		path := filepath.Join(homeDir, ".claude", "CLAUDE.md")
		if d, ok := readInstructionDoc(path, "claude", "user"); ok {
			add(d)
		}
	}
	return out, nil
}

func walkProjectInstructions(projectDir string) ([]InstructionDoc, error) {
	var out []InstructionDoc

	// Root-level AGENTS.md (codex).
	if d, ok := readInstructionDoc(filepath.Join(projectDir, "AGENTS.md"), "codex", "project"); ok {
		out = append(out, d)
	}

	// Nested CLAUDE.md walk — capped.
	count := 0
	walkErr := filepath.WalkDir(projectDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("skills: instruction walk error", "path", path, "err", err)
			return nil
		}
		if entry.IsDir() {
			// Skip well-known dependency dirs to keep the walk bounded in
			// real-world repos.
			name := entry.Name()
			if path != projectDir && (name == "node_modules" || name == ".git" || name == "vendor" || name == "dist" || name == "build") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "CLAUDE.md" {
			return nil
		}
		count++
		if count > maxInstructionFiles {
			slog.Warn("skills: instruction walk hit cap", "cap", maxInstructionFiles, "path", path)
			return filepath.SkipAll
		}
		if d, ok := readInstructionDoc(path, "claude", "project"); ok {
			out = append(out, d)
		}
		return nil
	})
	return out, walkErr
}

func readInstructionDoc(path, provider, scope string) (InstructionDoc, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return InstructionDoc{}, false
		}
		slog.Warn("skills: cannot read instruction doc", "path", path, "err", err)
		return InstructionDoc{}, false
	}
	name := filepath.Base(path)
	return InstructionDoc{
		Name:         name,
		Provider:     provider,
		Scope:        scope,
		FilePath:     path,
		ApproxTokens: len(body) / 4,
	}, true
}
