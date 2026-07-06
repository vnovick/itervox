package skills

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// scanHooks reads the `hooks` block from project and user `settings.json`.
// Returns one HookEntry per (event × matcher × command) tuple. Order within
// each event is preserved — hook firing order matters in Claude Code.
//
// homeDir == "" disables the user-home walk.
func scanHooks(projectDir, homeDir string) ([]HookEntry, error) {
	type entry struct {
		path   string
		source string
	}
	var sources []entry
	if projectDir != "" {
		sources = append(sources, entry{filepath.Join(projectDir, ".claude", "settings.json"), "project-settings"})
	}
	if homeDir != "" {
		sources = append(sources, entry{filepath.Join(homeDir, ".claude", "settings.json"), "user-settings"})
	}

	var out []HookEntry
	for _, s := range sources {
		hooks, err := readHooksFromJSON(s.path, s.source)
		if err != nil {
			return nil, err
		}
		out = append(out, hooks...)
	}
	return out, nil
}

// hookEntry shape inside the per-event array. We tolerate two shapes:
//
//  1. Flat: `{ "command": "...", "matcher": "Bash", "type": "command" }`
//  2. Nested (Claude Code's documented format):
//     `{ "matcher": "Bash", "hooks": [{ "type": "command", "command": "..." }] }`
type rawHook struct {
	Command string    `json:"command"`
	Matcher string    `json:"matcher"`
	Type    string    `json:"type"`
	Hooks   []rawHook `json:"hooks"`
}

type hooksEnvelope struct {
	Hooks map[string][]rawHook `json:"hooks"`
}

func readHooksFromJSON(path, source string) ([]HookEntry, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var env hooksEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		slog.Warn("skills: malformed hook-bearing settings.json", "path", path, "err", err)
		return nil, nil
	}
	if len(env.Hooks) == 0 {
		return nil, nil
	}

	var out []HookEntry
	for event, items := range env.Hooks {
		for _, item := range items {
			if len(item.Hooks) > 0 {
				// Nested form: matcher applies to each child hook.
				for _, child := range item.Hooks {
					if child.Command == "" {
						continue
					}
					out = append(out, makeHookEntry(event, item.Matcher, child.Command, source))
				}
			} else if item.Command != "" {
				// Flat form.
				out = append(out, makeHookEntry(event, item.Matcher, item.Command, source))
			}
		}
	}
	return out, nil
}

func makeHookEntry(event, matcher, command, source string) HookEntry {
	// Hook commands often expand into shell context — multiply for a
	// realistic context-cost estimate.
	tokens := (len(command) * 2) / 4
	return HookEntry{
		Event:        event,
		Matcher:      matcher,
		Command:      command,
		Provider:     "claude",
		Source:       source,
		ApproxTokens: tokens,
	}
}
