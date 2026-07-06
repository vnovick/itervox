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

// ParseCodexRuntime is the exported entry point used by the orchestrator
// adapter (T-102). Mirrors parseCodexRuntime exactly.
func ParseCodexRuntime(homeDir string, lookbackSessions int) (*RuntimeEvidenceSnapshot, error) {
	return parseCodexRuntime(homeDir, lookbackSessions)
}

// parseCodexRuntime is the Codex-side equivalent of parseClaudeRuntime.
// Walks `<homeDir>/.codex/history.jsonl` and `<homeDir>/.codex/sessions/**`
// for runtime evidence. Best-effort: a missing path returns an empty snapshot
// with no error.
//
// `lookbackSessions <= 0` defaults to 25. Aggregation rules match the Claude
// parser so the analytics layer can blend both providers transparently.
func parseCodexRuntime(homeDir string, lookbackSessions int) (*RuntimeEvidenceSnapshot, error) {
	if homeDir == "" {
		return emptyRuntimeSnap(), nil
	}
	if lookbackSessions <= 0 {
		lookbackSessions = 25
	}
	snap := emptyRuntimeSnap()

	historyPath := filepath.Join(homeDir, ".codex", "history.jsonl")
	if err := parseCodexJSONLInto(historyPath, snap); err != nil {
		return nil, err
	}

	sessionsRoot := filepath.Join(homeDir, ".codex", "sessions")
	sessions, err := collectClaudeSessions(sessionsRoot, lookbackSessions)
	if err != nil {
		// Sessions root missing is fine; collectClaudeSessions returns nil
		// inside its own walk. Surface only true I/O failures.
		return nil, err
	}
	for _, p := range sessions {
		snap.SourceLogPaths = append(snap.SourceLogPaths, p)
		if perr := parseCodexJSONLInto(p, snap); perr != nil {
			slog.Warn("skills: codex runtime parse failed", "path", p, "err", perr)
			continue
		}
	}
	snap.HasRuntimeEvidence = len(snap.SourceLogPaths) > 0
	return snap, nil
}

func emptyRuntimeSnap() *RuntimeEvidenceSnapshot {
	return &RuntimeEvidenceSnapshot{
		GeneratedAt:        time.Now(),
		CapabilityLoads:    make(map[string]int),
		HookExecutionCount: make(map[string]int),
		ToolCallCount:      make(map[string]int),
	}
}

// codexEventEnvelope mirrors the Codex event shape. Codex uses different
// field names than Claude (`tool_name`, `hook_event`, `loaded_skill`) so we
// have a separate envelope. Tolerates absent fields.
type codexEventEnvelope struct {
	Kind        string   `json:"kind"`
	ToolName    string   `json:"tool_name"`
	HookEvent   string   `json:"hook_event"`
	HookCommand string   `json:"hook_command"`
	LoadedSkill string   `json:"loaded_skill"`
	LoadedTools []string `json:"loaded_tools"`
	LoadedMCP   []string `json:"loaded_mcp"`
}

func parseCodexJSONLInto(path string, snap *RuntimeEvidenceSnapshot) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()

	// Track that we touched this file.
	if path != "" {
		snap.SourceLogPaths = appendUnique(snap.SourceLogPaths, path)
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev codexEventEnvelope
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		switch ev.Kind {
		case "system", "session_start":
			for _, t := range ev.LoadedTools {
				snap.CapabilityLoads[t]++
			}
			for _, m := range ev.LoadedMCP {
				snap.CapabilityLoads[m]++
			}
		case "skill_load":
			if ev.LoadedSkill != "" {
				snap.CapabilityLoads[ev.LoadedSkill]++
			}
		case "tool_call":
			if ev.ToolName != "" {
				snap.ToolCallCount[ev.ToolName]++
			}
		case "hook":
			if ev.HookEvent != "" {
				key := ev.HookEvent
				if ev.HookCommand != "" {
					key = ev.HookEvent + "|" + ev.HookCommand
				}
				snap.HookExecutionCount[key]++
			}
		}
	}
	return sc.Err()
}

func appendUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}

// MergeRuntimeSnapshots combines snapshots from multiple providers. Used by
// the analytics engine (T-100) to blend Claude + Codex evidence into a
// single per-capability stat surface.
func MergeRuntimeSnapshots(snaps ...*RuntimeEvidenceSnapshot) *RuntimeEvidenceSnapshot {
	out := emptyRuntimeSnap()
	for _, s := range snaps {
		if s == nil {
			continue
		}
		out.HasRuntimeEvidence = out.HasRuntimeEvidence || s.HasRuntimeEvidence || len(s.SourceLogPaths) > 0
		for k, v := range s.CapabilityLoads {
			out.CapabilityLoads[k] += v
		}
		for k, v := range s.HookExecutionCount {
			out.HookExecutionCount[k] += v
		}
		for k, v := range s.ToolCallCount {
			out.ToolCallCount[k] += v
		}
		out.SourceLogPaths = append(out.SourceLogPaths, s.SourceLogPaths...)
	}
	sort.Strings(out.SourceLogPaths)
	return out
}
