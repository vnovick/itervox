package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/vnovick/itervox/internal/agent"
)

// repoInfo holds values discovered by scanning the current directory.
type repoInfo struct {
	RemoteURL     string // raw git remote URL
	Owner         string // e.g. "vnovick"
	Repo          string // e.g. "itervox"
	CloneURL      string // SSH clone URL reconstructed for after_create hook
	DefaultBranch string // "main" or "master"
	ProjectName   string // repo name, used for workspace.root
	HasClaudeMD   bool   // CLAUDE.md present in dir
	HasAgentsMD   bool   // AGENTS.md present in dir
	Stacks        []detectedStack
	ClaudeModels  []agent.ModelOption // discovered Claude models (may be empty)
	CodexModels   []agent.ModelOption // discovered Codex models (may be empty)
}

type detectedStack struct {
	Name     string
	Commands []string
}

// scanRepo inspects dir (typically ".") for git remote, branch, CLAUDE.md, and
// language/framework indicators. All fields fall back to sensible placeholders
// so the output is always valid even in a non-git directory.
func scanRepo(dir string) repoInfo {
	info := repoInfo{DefaultBranch: "main", ProjectName: "my-project"}

	if out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output(); err == nil {
		info.RemoteURL = strings.TrimSpace(string(out))
		info.Owner, info.Repo = parseGitRemote(info.RemoteURL)
		if info.Repo != "" {
			info.ProjectName = info.Repo
		}
		if info.Owner != "" && info.Repo != "" {
			info.CloneURL = fmt.Sprintf("git@github.com:%s/%s.git", info.Owner, info.Repo)
		}
	}

	if out, err := exec.Command("git", "-C", dir, "symbolic-ref", "refs/remotes/origin/HEAD").Output(); err == nil {
		ref := strings.TrimSpace(string(out))
		if parts := strings.Split(ref, "/"); len(parts) > 0 {
			info.DefaultBranch = parts[len(parts)-1]
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); err == nil {
		info.HasClaudeMD = true
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err == nil {
		info.HasAgentsMD = true
	}
	info.Stacks = detectStacks(dir)

	return info
}

// parseGitRemote extracts owner and repo from an SSH or HTTPS git remote URL.
func parseGitRemote(remote string) (owner, repo string) {
	remote = strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if strings.HasPrefix(remote, "git@") {
		if _, path, ok := strings.Cut(remote, ":"); ok {
			owner, repo, _ = strings.Cut(path, "/")
			return
		}
	}
	parts := strings.Split(remote, "/")
	if len(parts) >= 2 {
		repo = parts[len(parts)-1]
		owner = parts[len(parts)-2]
	}
	return
}

// detectStacks scans dir for language/framework indicator files and returns
// the detected stacks with their suggested check commands.
func detectStacks(dir string) []detectedStack {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}

	var stacks []detectedStack
	if has("go.mod") {
		stacks = append(stacks, detectedStack{
			Name:     "Go",
			Commands: []string{"go test ./...", "go vet ./..."},
		})
	}
	if has("package.json") {
		stacks = append(stacks, detectedStack{
			Name:     "Node.js",
			Commands: detectNodeCommands(dir),
		})
	}
	if has("Cargo.toml") {
		stacks = append(stacks, detectedStack{
			Name:     "Rust",
			Commands: []string{"cargo test", "cargo clippy -- -D warnings"},
		})
	}
	if has("pyproject.toml") || has("setup.py") || has("requirements.txt") {
		stacks = append(stacks, detectedStack{
			Name:     "Python",
			Commands: []string{"python -m pytest", "python -m mypy ."},
		})
	}
	if has("mix.exs") {
		stacks = append(stacks, detectedStack{
			Name:     "Elixir",
			Commands: []string{"mix test", "mix credo"},
		})
	}
	if has("Gemfile") {
		stacks = append(stacks, detectedStack{
			Name:     "Ruby",
			Commands: []string{"bundle exec rspec", "bundle exec rubocop"},
		})
	}
	return stacks
}

// detectNodeCommands reads package.json scripts to suggest the right test/lint commands.
func detectNodeCommands(dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return []string{"pnpm test"}
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return []string{"pnpm test"}
	}

	var cmds []string
	for _, script := range []string{"test", "lint", "typecheck", "check", "build"} {
		if _, ok := pkg.Scripts[script]; ok {
			cmds = append(cmds, "pnpm "+script)
		}
	}
	if len(cmds) == 0 {
		cmds = []string{"pnpm test"}
	}
	return cmds
}
