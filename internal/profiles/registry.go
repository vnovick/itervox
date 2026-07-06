// Package profiles owns the embedded built-in agent profile files (SOUL.md,
// INSTRUCTIONS.md) and the registry that resolves a profile name to its
// embedded content. File-on-disk profiles always win; built-ins are the
// fallback that lets an operator reference a profile (e.g. merge-bot) without
// authoring the SOUL/INSTRUCTIONS files themselves.
package profiles

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

//go:embed builtin
var builtinFS embed.FS

// Builtin describes one shipped profile. Defaults are applied only when the
// corresponding WORKFLOW.md field is unset.
type Builtin struct {
	Name             string
	Soul             string
	Instructions     string
	DefaultCommand   string
	DefaultBackend   string
	DefaultActions   []string
	SoulFilePath     string
	InstructionsPath string
}

// builtinDefaults centralises per-profile defaults. Keep this list aligned
// with the directories under internal/profiles/builtin/.
var builtinDefaults = map[string]struct {
	Command string
	Backend string
	Actions []string
}{
	"merge-bot": {
		Command: "claude --model claude-haiku-4-5-20251001",
		Backend: "claude",
		Actions: []string{"comment", "comment_pr", "merge_pr", "move_state"},
	},
}

// Lookup returns the built-in profile for name, or nil if no built-in exists.
func Lookup(name string) *Builtin {
	if name == "" {
		return nil
	}
	dir := "builtin/" + name
	soulBytes, err := fs.ReadFile(builtinFS, dir+"/SOUL.md")
	if err != nil {
		return nil
	}
	instBytes, err := fs.ReadFile(builtinFS, dir+"/INSTRUCTIONS.md")
	if err != nil {
		return nil
	}
	out := &Builtin{
		Name:             name,
		Soul:             strings.TrimSpace(string(soulBytes)),
		Instructions:     strings.TrimSpace(string(instBytes)),
		SoulFilePath:     ".itervox/agents/" + name + "/SOUL.md",
		InstructionsPath: ".itervox/agents/" + name + "/INSTRUCTIONS.md",
	}
	if defaults, ok := builtinDefaults[name]; ok {
		out.DefaultCommand = defaults.Command
		out.DefaultBackend = defaults.Backend
		out.DefaultActions = append([]string(nil), defaults.Actions...)
	}
	return out
}

// Names returns the sorted list of shipped built-in profile names.
func Names() []string {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if Lookup(entry.Name()) != nil {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// IsBuiltin reports whether name refers to a shipped built-in profile.
func IsBuiltin(name string) bool {
	return Lookup(name) != nil
}
