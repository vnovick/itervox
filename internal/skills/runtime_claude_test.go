package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleSession = `{"type":"system","system":{"tools":["Read","Edit","Bash"],"mcp_servers":["ctx7","filesystem"],"skills_loaded":["my-skill","ralph-loop"]}}
{"type":"tool_use","tool":"Read"}
{"type":"tool_use","tool":"Edit"}
{"type":"tool_use","tool":"Read"}
{"type":"hook","hook":{"event":"PreToolUse","command":"echo hi"}}
malformed line should be skipped
{"type":"text","text":"hello"}
{"type":"hook","hook":{"event":"PreToolUse","command":"echo hi"}}
`

func TestParseClaudeRuntime_AggregatesCounts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "issue-1", "session-a.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(sessionPath, []byte(sampleSession), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	snap, err := parseClaudeRuntime(dir, 25)
	if err != nil {
		t.Fatalf("parseClaudeRuntime: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snap.CapabilityLoads["Read"] != 1 || snap.CapabilityLoads["ctx7"] != 1 || snap.CapabilityLoads["my-skill"] != 1 {
		t.Errorf("unexpected capability loads: %v", snap.CapabilityLoads)
	}
	if snap.ToolCallCount["Read"] != 2 || snap.ToolCallCount["Edit"] != 1 {
		t.Errorf("unexpected tool counts: %v", snap.ToolCallCount)
	}
	if snap.HookExecutionCount["PreToolUse|echo hi"] != 2 {
		t.Errorf("unexpected hook counts: %v", snap.HookExecutionCount)
	}
	if len(snap.SourceLogPaths) != 1 {
		t.Errorf("expected 1 source path, got %d", len(snap.SourceLogPaths))
	}
}

func TestParseClaudeRuntime_LookbackLimitsByMtime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create 3 sessions with stepped mtimes; lookback=2 should pick the
	// 2 most recent.
	for i, name := range []string{"old.jsonl", "mid.jsonl", "new.jsonl"} {
		path := filepath.Join(dir, "issue-1", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(sampleSession), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		// stagger mtimes
		mt := time.Now().Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	snap, err := parseClaudeRuntime(dir, 2)
	if err != nil {
		t.Fatalf("parseClaudeRuntime: %v", err)
	}
	if len(snap.SourceLogPaths) != 2 {
		t.Errorf("expected 2 sessions in lookback, got %d", len(snap.SourceLogPaths))
	}
}

func TestParseClaudeRuntime_MissingDirReturnsEmpty(t *testing.T) {
	t.Parallel()
	snap, err := parseClaudeRuntime(filepath.Join(t.TempDir(), "nope"), 0)
	if err != nil {
		t.Fatalf("expected no error on missing dir, got %v", err)
	}
	if snap == nil || len(snap.SourceLogPaths) != 0 {
		t.Errorf("expected empty snapshot, got %+v", snap)
	}
}

func TestParseClaudeRuntime_DefaultLookbackIs25(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for i := 0; i < 30; i++ {
		path := filepath.Join(dir, "issue-1", "s"+string(rune('A'+i))+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(sampleSession), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		mt := time.Now().Add(time.Duration(i) * time.Minute)
		_ = os.Chtimes(path, mt, mt)
	}
	snap, err := parseClaudeRuntime(dir, 0)
	if err != nil {
		t.Fatalf("parseClaudeRuntime: %v", err)
	}
	if len(snap.SourceLogPaths) != 25 {
		t.Errorf("expected default cap of 25, got %d", len(snap.SourceLogPaths))
	}
}
