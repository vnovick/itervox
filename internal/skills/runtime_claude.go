package skills

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ParseClaudeRuntime is the exported entry point used by the orchestrator
// adapter (T-102). Mirrors parseClaudeRuntime exactly.
func ParseClaudeRuntime(logDir string, lookbackSessions int) (*RuntimeEvidenceSnapshot, error) {
	return parseClaudeRuntime(logDir, lookbackSessions)
}

// parseClaudeRuntime walks the most recent `lookbackSessions` JSONL session
// logs under `logDir` (typically `~/.itervox/logs/<issue>/<session>.jsonl`)
// and aggregates capability-load + tool-call counts into a
// RuntimeEvidenceSnapshot.
//
// Tolerates malformed lines by skipping with `slog.Warn`. A missing logDir
// returns an empty snapshot with `SourceLogPaths: nil`, never an error.
//
// `lookbackSessions <= 0` defaults to 25.
func parseClaudeRuntime(logDir string, lookbackSessions int) (*RuntimeEvidenceSnapshot, error) {
	if lookbackSessions <= 0 {
		lookbackSessions = 25
	}
	snap := &RuntimeEvidenceSnapshot{
		GeneratedAt:        time.Now(),
		CapabilityLoads:    make(map[string]int),
		HookExecutionCount: make(map[string]int),
		ToolCallCount:      make(map[string]int),
	}

	info, err := os.Stat(logDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return snap, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return snap, nil
	}

	sessions, err := collectClaudeSessions(logDir, lookbackSessions)
	if err != nil {
		return nil, err
	}
	for _, path := range sessions {
		snap.SourceLogPaths = append(snap.SourceLogPaths, path)
		if perr := parseClaudeSessionInto(path, snap); perr != nil {
			slog.Warn("skills: parse session failed", "path", path, "err", perr)
			continue
		}
	}
	snap.HasRuntimeEvidence = len(snap.SourceLogPaths) > 0
	return snap, nil
}

// collectClaudeSessions returns up to `limit` `.jsonl` paths under root
// sorted newest-first by mtime.
func collectClaudeSessions(root string, limit int) ([]string, error) {
	type entry struct {
		path string
		mod  time.Time
	}
	var entries []entry
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("skills: claude runtime walk error", "path", path, "err", err)
			return nil
		}
		if d.IsDir() || filepath.Ext(d.Name()) != ".jsonl" {
			return nil
		}
		fi, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		entries = append(entries, entry{path, fi.ModTime()})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod.After(entries[j].mod) })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.path
	}
	return out, nil
}

// claudeEventEnvelope captures the relevant subset of fields we care about.
// JSONL streams from Claude Code emit one JSON object per line with a
// `type` discriminator. We only inspect a handful.
type claudeEventEnvelope struct {
	Type   string `json:"type"`
	Tool   string `json:"tool"`
	Event  string `json:"event"`
	System *struct {
		Tools        []string `json:"tools"`
		MCPServers   []string `json:"mcp_servers"`
		SkillsLoaded []string `json:"skills_loaded"`
	} `json:"system"`
	Hook *struct {
		Event   string `json:"event"`
		Command string `json:"command"`
	} `json:"hook"`
}

func parseClaudeSessionInto(path string, snap *RuntimeEvidenceSnapshot) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	// Some session lines are very long (full tool outputs); raise the buffer.
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev claudeEventEnvelope
		if err := json.Unmarshal(line, &ev); err != nil {
			// Malformed line — skip silently; full warn-spam isn't useful.
			continue
		}
		switch ev.Type {
		case "system":
			if ev.System != nil {
				for _, t := range ev.System.Tools {
					snap.CapabilityLoads[t]++
				}
				for _, m := range ev.System.MCPServers {
					snap.CapabilityLoads[m]++
				}
				for _, s := range ev.System.SkillsLoaded {
					snap.CapabilityLoads[s]++
				}
			}
		case "tool_use", "tool":
			if ev.Tool != "" {
				snap.ToolCallCount[ev.Tool]++
			}
		case "hook":
			if ev.Hook != nil && ev.Hook.Event != "" {
				key := ev.Hook.Event
				if ev.Hook.Command != "" {
					key = ev.Hook.Event + "|" + ev.Hook.Command
				}
				snap.HookExecutionCount[key]++
			}
		}
	}
	return sc.Err()
}
