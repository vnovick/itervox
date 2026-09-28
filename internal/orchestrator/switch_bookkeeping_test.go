package orchestrator

// CORE-052 — per-issue rate_limited switch bookkeeping (switch history,
// cooldowns, cap-comment dedupe) lives in State and is persisted with the
// auto-switched overrides in a versioned envelope.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBookkeepingOrch(t *testing.T, path string) *Orchestrator {
	t.Helper()
	cfg := testConfig()
	cfg.Agent.MaxSwitchesPerIssuePerWindow = 2
	cfg.Agent.SwitchWindowHours = 6
	o := New(cfg, nil, nil, nil)
	o.SetAutoSwitchedFile(path)
	return o
}

// TestSwitchHistory_PersistRoundtrip: every bookkeeping map survives a save
// and a fresh orchestrator's load, and the per-issue cap therefore holds
// across a restart.
func TestSwitchHistory_PersistRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto_switched.json")
	now := time.Now().Truncate(time.Second)

	o := newBookkeepingOrch(t, path)
	state := NewState(o.cfg)
	state.IssueProfiles["ENG-1"] = "fallback"
	state.IssueBackends["ENG-1"] = "codex"
	state.AutoSwitchedIdentifiers["ENG-1"] = struct{}{}
	state.AutoSwitchedAt["ENG-1"] = now.Add(-time.Hour)

	require.True(t, o.allowRateLimitSwitch(&state, "id1", now))
	o.recordRateLimitSwitch(&state, "id1", now.Add(-2*time.Hour))
	require.True(t, o.allowRateLimitSwitch(&state, "id1", now))
	o.recordRateLimitSwitch(&state, "id1", now.Add(-time.Hour))
	require.False(t, o.allowRateLimitSwitch(&state, "id1", now), "cap of 2 reached before restart")
	o.setRateLimitCooldown(&state, "id1|default", now.Add(30*time.Minute))
	require.True(t, o.claimRateLimitCapComment(&state, "id1", now))
	o.saveAutoSwitchedToDisk(&state)

	// "Restart": a new orchestrator loads the same file into a fresh State.
	o2 := newBookkeepingOrch(t, path)
	loaded := o2.loadAutoSwitchedFromDisk(NewState(o2.cfg))

	assert.Equal(t, "fallback", loaded.IssueProfiles["ENG-1"])
	assert.Equal(t, "codex", loaded.IssueBackends["ENG-1"])
	assert.Contains(t, loaded.AutoSwitchedIdentifiers, "ENG-1")
	require.Len(t, loaded.SwitchHistory["id1"], 2, "switch history must survive the restart")
	assert.True(t, now.Add(-2*time.Hour).Equal(loaded.SwitchHistory["id1"][0]))
	until, ok := o2.rateLimitCooldownUntil(loaded, "id1|default")
	require.True(t, ok, "cooldown must survive the restart")
	assert.True(t, now.Add(30*time.Minute).Equal(until))
	assert.Contains(t, loaded.RateLimitCapCommentUntil, "id1", "cap-comment dedupe must survive the restart")

	assert.False(t, o2.allowRateLimitSwitch(&loaded, "id1", now),
		"the per-issue switch cap must still hold after a restart")
	assert.False(t, o2.claimRateLimitCapComment(&loaded, "id1", now.Add(time.Minute)),
		"the cap comment must not be re-posted after a restart")

	// The file is the versioned envelope.
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var env map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &env))
	assert.JSONEq(t, `2`, string(env["version"]))
	for _, k := range []string{"overrides", "switch_history", "cooldowns", "cap_comment_until"} {
		assert.Contains(t, env, k)
	}
}

// TestAutoSwitched_LoadsLegacyFlatMap: a v1 flat file yields the same
// overrides as before, and the next save rewrites it as the v2 envelope.
func TestAutoSwitched_LoadsLegacyFlatMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto_switched.json")
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	legacy := `{"ENG-1":{"profile":"fallback","backend":"codex","switched_at":"2026-09-20T10:00:00Z"},"ENG-2":{"profile":"other"}}`
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o644))

	o := newBookkeepingOrch(t, path)
	state := o.loadAutoSwitchedFromDisk(NewState(o.cfg))
	assert.Equal(t, map[string]string{"ENG-1": "fallback", "ENG-2": "other"}, state.IssueProfiles)
	assert.Equal(t, map[string]string{"ENG-1": "codex"}, state.IssueBackends)
	assert.Len(t, state.AutoSwitchedIdentifiers, 2)
	assert.True(t, at.Equal(state.AutoSwitchedAt["ENG-1"]))
	assert.Empty(t, state.SwitchHistory)

	o.saveAutoSwitchedToDisk(&state)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"version":2`, "the legacy file is migrated on the next save")

	// The migrated file reloads to the same overrides with no phantom
	// identifiers such as "version" or "switch_history".
	again := o.loadAutoSwitchedFromDisk(NewState(o.cfg))
	ids := make([]string, 0, len(again.AutoSwitchedIdentifiers))
	for id := range again.AutoSwitchedIdentifiers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	assert.Equal(t, []string{"ENG-1", "ENG-2"}, ids)
	for _, phantom := range []string{"version", "overrides", "switch_history", "cooldowns", "cap_comment_until"} {
		assert.NotContains(t, again.IssueProfiles, phantom)
	}
}

// TestSwitchHistory_PrunedOnLoad: stamps older than the window, expired
// cooldowns and expired cap-comment entries are dropped when the file is
// loaded.
func TestSwitchHistory_PrunedOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto_switched.json")
	now := time.Now().UTC().Truncate(time.Second)
	env := map[string]any{
		"version":   2,
		"overrides": map[string]any{},
		"switch_history": map[string][]time.Time{
			"old":   {now.Add(-10 * time.Hour)},
			"mixed": {now.Add(-9 * time.Hour), now.Add(-time.Hour)},
		},
		"cooldowns":         map[string]time.Time{"old|p": now.Add(-time.Minute), "live|p": now.Add(time.Hour)},
		"cap_comment_until": map[string]time.Time{"old": now.Add(-time.Minute), "live": now.Add(time.Hour)},
	}
	data, err := json.Marshal(env)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	o := newBookkeepingOrch(t, path) // window 6h
	state := o.loadAutoSwitchedFromDisk(NewState(o.cfg))
	assert.NotContains(t, state.SwitchHistory, "old")
	require.Len(t, state.SwitchHistory["mixed"], 1)
	assert.True(t, now.Add(-time.Hour).Equal(state.SwitchHistory["mixed"][0]))
	assert.Equal(t, []string{"live|p"}, sortedKeys(state.RateLimitCooldowns))
	assert.Equal(t, []string{"live"}, sortedKeys(state.RateLimitCapCommentUntil))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestSwitchBookkeeping_WritersTakeState is the structural half of "only the
// event loop mutates it": every write to the three State maps is inside a
// function that receives the State (never a field of Orchestrator), and the
// set of writer functions is exactly the allowlist below — each reached only
// from event-loop handlers (dispatchMatchingRateLimitedAutomations, the tick
// janitor, and startup load before the loop's first select).
func TestSwitchBookkeeping_WritersTakeState(t *testing.T) {
	allowed := map[string]bool{
		"allowRateLimitSwitch":      true,
		"recordRateLimitSwitch":     true,
		"claimRateLimitCapComment":  true,
		"setRateLimitCooldown":      true,
		"pruneRateLimitedMaps":      true,
		"applyAutoSwitchedEnvelope": true,
		// Clone assigns the fields of its own fresh copy, never the source.
		"Clone": true,
	}
	fields := map[string]bool{"SwitchHistory": true, "RateLimitCooldowns": true, "RateLimitCapCommentUntil": true}

	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	writers := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				var targets []ast.Expr
				switch x := n.(type) {
				case *ast.AssignStmt:
					targets = x.Lhs
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "delete" && len(x.Args) > 0 {
						targets = x.Args[:1]
					}
				case *ast.GoStmt:
					if touchesFields(x, fields) {
						t.Errorf("%s: %s touches switch bookkeeping inside a go statement", fset.Position(x.Pos()), fn.Name.Name)
					}
				}
				for _, tgt := range targets {
					if name := bookkeepingField(tgt, fields); name != "" {
						writers[fn.Name.Name] = true
						if !allowed[fn.Name.Name] {
							t.Errorf("%s: %s writes State.%s outside the allowlisted event-loop helpers",
								fset.Position(tgt.Pos()), fn.Name.Name, name)
						}
					}
				}
				return true
			})
		}
	}
	assert.NotEmpty(t, writers, "the writers must exist")
}

func bookkeepingField(e ast.Expr, fields map[string]bool) string {
	for {
		switch x := e.(type) {
		case *ast.IndexExpr:
			e = x.X
		case *ast.SelectorExpr:
			if fields[x.Sel.Name] {
				return x.Sel.Name
			}
			return ""
		default:
			return ""
		}
	}
}

func touchesFields(n ast.Node, fields map[string]bool) bool {
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		if s, ok := m.(*ast.SelectorExpr); ok && fields[s.Sel.Name] {
			found = true
		}
		return !found
	})
	return found
}

// TestSwitchBookkeeping_SnapshotIsIsolated is the runtime half: the maps are
// deep-copied into snapshots (State.Clone), so a reader goroutine iterating a
// snapshot never races the loop's writes (run under -race).
func TestSwitchBookkeeping_SnapshotIsIsolated(t *testing.T) {
	o := newBookkeepingOrch(t, "")
	state := NewState(o.cfg)
	now := time.Now()
	o.recordRateLimitSwitch(&state, "id1", now)
	o.setRateLimitCooldown(&state, "id1|p", now.Add(time.Hour))
	o.claimRateLimitCapComment(&state, "id1", now)
	snap := state.Clone()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = len(snap.SwitchHistory["id1"])
			for range snap.RateLimitCooldowns {
			}
			for range snap.RateLimitCapCommentUntil {
			}
		}
	}()
	for i := 0; i < 200; i++ {
		o.recordRateLimitSwitch(&state, "id1", now.Add(time.Duration(i)*time.Millisecond))
		o.setRateLimitCooldown(&state, "id1|p", now.Add(time.Duration(i)*time.Second))
		state.RateLimitCapCommentUntil["id2"] = now
	}
	wg.Wait()
	assert.Len(t, snap.SwitchHistory["id1"], 1, "the snapshot must not see later writes")
}
