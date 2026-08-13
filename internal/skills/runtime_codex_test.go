package skills

import (
	"os"
	"path/filepath"
	"testing"
)

const codexHistory = `{"kind":"tool_call","tool_name":"Read"}
{"kind":"tool_call","tool_name":"Read"}
{"kind":"tool_call","tool_name":"Edit"}
{"kind":"hook","hook_event":"PostRun","hook_command":"echo done"}
{"kind":"skill_load","loaded_skill":"my-skill"}
not-json
{"kind":"system","loaded_tools":["Read","Edit"],"loaded_mcp":["fs"]}
`

func TestParseCodexRuntime_HistoryAndSessions(t *testing.T) {
	t.Parallel()
	user := t.TempDir()

	// history.jsonl
	historyPath := filepath.Join(user, ".codex", "history.jsonl")
	if err := os.MkdirAll(filepath.Dir(historyPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(historyPath, []byte(codexHistory), 0o644); err != nil {
		t.Fatalf("write history: %v", err)
	}

	// sessions/<id>.jsonl
	sessionsDir := filepath.Join(user, ".codex", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "session-a.jsonl"), []byte(codexHistory), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	snap, err := parseCodexRuntime(user, 25)
	if err != nil {
		t.Fatalf("parseCodexRuntime: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
		return
	}
	// history + session both contributed; counts should be doubled vs. single file.
	if snap.ToolCallCount["Read"] != 4 {
		t.Errorf("expected 4 Read calls (2 history + 2 session), got %d", snap.ToolCallCount["Read"])
	}
	if snap.HookExecutionCount["PostRun|echo done"] != 2 {
		t.Errorf("expected 2 PostRun hook executions, got %d", snap.HookExecutionCount["PostRun|echo done"])
	}
	if snap.CapabilityLoads["my-skill"] != 2 {
		t.Errorf("expected 2 my-skill loads, got %d", snap.CapabilityLoads["my-skill"])
	}
	if len(snap.SourceLogPaths) < 1 {
		t.Errorf("expected at least 1 source path, got %d", len(snap.SourceLogPaths))
	}
	if !snap.HasRuntimeEvidence {
		t.Errorf("expected HasRuntimeEvidence=true")
	}
}

func TestParseCodexRuntime_MissingHomeDir(t *testing.T) {
	t.Parallel()
	snap, err := parseCodexRuntime("", 0)
	if err != nil {
		t.Fatalf("expected no error on empty homeDir, got %v", err)
	}
	if snap == nil || len(snap.SourceLogPaths) != 0 {
		t.Errorf("expected empty snapshot, got %+v", snap)
	}
	if snap.HasRuntimeEvidence {
		t.Errorf("expected HasRuntimeEvidence=false without source logs")
	}
}

func TestParseCodexRuntime_AllPathsAbsent(t *testing.T) {
	t.Parallel()
	snap, err := parseCodexRuntime(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(snap.CapabilityLoads) != 0 || len(snap.ToolCallCount) != 0 {
		t.Errorf("expected empty maps, got %+v", snap)
	}
	if snap.HasRuntimeEvidence {
		t.Errorf("expected HasRuntimeEvidence=false without source logs")
	}
}

func TestMergeRuntimeSnapshots(t *testing.T) {
	t.Parallel()
	a := emptyRuntimeSnap()
	a.ToolCallCount["Read"] = 3
	a.SourceLogPaths = []string{"/a"}
	b := emptyRuntimeSnap()
	b.ToolCallCount["Read"] = 2
	b.ToolCallCount["Edit"] = 1
	b.SourceLogPaths = []string{"/b"}
	out := MergeRuntimeSnapshots(a, b, nil)
	if out.ToolCallCount["Read"] != 5 {
		t.Errorf("expected merged Read=5, got %d", out.ToolCallCount["Read"])
	}
	if out.ToolCallCount["Edit"] != 1 {
		t.Errorf("expected merged Edit=1, got %d", out.ToolCallCount["Edit"])
	}
	if len(out.SourceLogPaths) != 2 {
		t.Errorf("expected 2 source paths, got %d", len(out.SourceLogPaths))
	}
	if !out.HasRuntimeEvidence {
		t.Errorf("expected merged runtime evidence flag")
	}
}
