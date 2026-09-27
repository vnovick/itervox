package workflow_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/workflow"
	"gopkg.in/yaml.v3"
)

// assertValidWorkflowYAML extracts the YAML front matter from a workflow
// file's contents and confirms it parses cleanly. The original indent-patching
// bug symptom was a mid-block indent mismatch that produced "mapping values
// are not allowed in this context" at parse time.
func assertValidWorkflowYAML(t *testing.T, contents string) {
	t.Helper()
	body := strings.TrimPrefix(contents, "---\n")
	end := strings.Index(body, "\n---\n")
	if end < 0 {
		t.Fatalf("expected closing front-matter delimiter, got:\n%s", contents)
	}
	front := body[:end]
	var into map[string]any
	if err := yaml.Unmarshal([]byte(front), &into); err != nil {
		t.Fatalf("front matter must be valid YAML after Patch*; got error %v\n--- front matter ---\n%s", err, front)
	}
}

func TestLoadBasicWorkflow(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "workflows", "basic.md")
	wf, err := workflow.Load(path)
	require.NoError(t, err)
	assert.NotNil(t, wf.Config)
	assert.Contains(t, wf.PromptTemplate, "issue.identifier")
	trackerKind, ok := wf.Config["tracker"].(map[string]interface{})
	require.True(t, ok, "tracker should be a map")
	assert.Equal(t, "linear", trackerKind["kind"])
}

func TestLoadNoFrontMatter(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "workflows", "no-front-matter.md")
	wf, err := workflow.Load(path)
	require.NoError(t, err)
	assert.Empty(t, wf.Config)
	assert.Contains(t, wf.PromptTemplate, "no front matter")
}

func TestLoadMissingFile(t *testing.T) {
	_, err := workflow.Load("/nonexistent/path/WORKFLOW.md")
	require.Error(t, err)
	var wfErr *workflow.Error
	require.ErrorAs(t, err, &wfErr)
	assert.Equal(t, workflow.ErrMissingFile, wfErr.Code)
}

func TestLoadInvalidYAML(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "workflows", "invalid-yaml.md")
	_, err := workflow.Load(path)
	require.Error(t, err)
	var wfErr *workflow.Error
	require.ErrorAs(t, err, &wfErr)
	assert.Equal(t, workflow.ErrParseError, wfErr.Code)
}

func TestLoadFrontMatterNotAMap(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	content := "---\n- item1\n- item2\n---\n\nPrompt body.\n"
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	_, err := workflow.Load(f)
	require.Error(t, err)
	var wfErr *workflow.Error
	require.ErrorAs(t, err, &wfErr)
	assert.Equal(t, workflow.ErrFrontMatterNotAMap, wfErr.Code)
}

func TestLoadEmptyFrontMatter(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	content := "---\n---\n\nSome prompt.\n"
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	wf, err := workflow.Load(f)
	require.NoError(t, err)
	assert.Empty(t, wf.Config)
	assert.Equal(t, "Some prompt.", wf.PromptTemplate)
}

func TestLoadPromptIsTrimmed(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	content := "---\ntracker:\n  kind: linear\n---\n\n\n  hello  \n\n"
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	wf, err := workflow.Load(f)
	require.NoError(t, err)
	assert.Equal(t, "hello", wf.PromptTemplate)
}

func TestPatchIntField(t *testing.T) {
	content := "---\nagent:\n  max_concurrent_agents: 3\n  max_turns: 60\n---\n\nPrompt body.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	require.NoError(t, workflow.PatchIntField(f, "max_concurrent_agents", 7))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "max_concurrent_agents: 7")
	assert.Contains(t, got, "max_turns: 60") // unchanged
	assert.Contains(t, got, "Prompt body.")  // body preserved
}

func TestPatchIntFieldKeyNotFound(t *testing.T) {
	content := "---\nagent:\n  max_turns: 60\n---\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	err := workflow.PatchIntField(f, "max_concurrent_agents", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPatchIntFieldPreservesComments(t *testing.T) {
	content := "---\nagent:\n  max_concurrent_agents: 3 # set at runtime\n---\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	require.NoError(t, workflow.PatchIntField(f, "max_concurrent_agents", 10))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	assert.Contains(t, string(data), "max_concurrent_agents: 10 # set at runtime")
}

// TestPatchIntFieldConcurrent verifies T-46 (gaps_280426 06.G-01): concurrent
// PatchIntField calls on the same path serialize via editMu, so the final
// file always contains exactly one of the requested values rather than a
// torn / partially-overwritten line. Pre-fix, this test would race the
// read-modify-write on a busy filesystem and could produce an unexpected
// final value or a corrupt file.
func TestPatchIntFieldConcurrent(t *testing.T) {
	content := "---\nagent:\n  max_concurrent_agents: 0\n---\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	const concurrency = 10
	values := make([]int, concurrency)
	for i := range values {
		values[i] = i + 1
	}

	var wg sync.WaitGroup
	for _, v := range values {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			require.NoError(t, workflow.PatchIntField(f, "max_concurrent_agents", n))
		}(v)
	}
	wg.Wait()

	// Final file content must contain exactly one of the written values
	// (whichever goroutine ran last under the lock) — not a malformed line.
	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	matched := false
	for _, v := range values {
		expected := fmt.Sprintf("max_concurrent_agents: %d", v)
		if strings.Contains(got, expected) {
			matched = true
			break
		}
	}
	assert.True(t, matched, "expected file to contain one of the written values, got:\n%s", got)
}

// TestConcurrentPatchAndProfileSaveNoLostUpdate is CORE-006's headline
// regression test: before the fix, patchBlockBoolField, PatchAgentStringField
// and PatchDependenciesStringField each did an unlocked read-modify-write, so
// two overlapping edits to the SAME file (one tab's Settings save racing
// another's, or the TUI racing the dashboard) could have one rename clobber
// the other's change. 60 rounds x 5 concurrent writers = 300 goroutines,
// matching the original repro's "iters=300 lost_updates=300" scale. Every
// writer sets an idempotent target value, so — with the lock — the exact
// interleaving doesn't matter: the final file must contain ALL five edits
// every time, not just the ones from whichever goroutine wrote last.
//
// NOTE (M0-close G9): this is a corruption/duplicate-header stress test, not
// the lost-update coverage. Every write is an idempotent SET repeated across
// rounds on one shared file, so a write lost in any round but the last is
// healed by a later round before the final assertion; only a loss in the
// final overlapping round is observable, which makes its lost-update signal
// incidental (it does fail with a no-op lockForPath today, but nothing
// guarantees it). Per-writer, one-write-per-key lost-update coverage is
// TestPatchWritersDoNotLoseAnUpdateUnderInterleavedReadModifyWrite.
func TestConcurrentPatchAndProfileSaveNoLostUpdate(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  command: claude\nworkspace:\n  root: /tmp/ws\n---\n\nBody.\n")

	const rounds = 60
	var wg sync.WaitGroup
	errCh := make(chan error, rounds*5)
	record := func(err error) {
		if err != nil {
			errCh <- err
		}
	}
	for i := 0; i < rounds; i++ {
		wg.Add(5)
		go func() { defer wg.Done(); record(workflow.PatchAgentBoolField(f, "verbose", true)) }()
		go func() { defer wg.Done(); record(workflow.PatchWorkspaceBoolField(f, "auto_clear", true)) }()
		go func() { defer wg.Done(); record(workflow.PatchAgentStringField(f, "backend", "codex")) }()
		go func() {
			defer wg.Done()
			record(workflow.PatchDependenciesStringField(f, "analysis_mode", "manual"))
		}()
		go func() {
			defer wg.Done()
			profiles := map[string]workflow.ProfileEntry{"reviewer": {Command: "claude --model opus"}}
			record(workflow.PatchProfilesBlock(f, profiles))
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "verbose: true", "PatchAgentBoolField's edit must survive")
	assert.Contains(t, got, "auto_clear: true", "PatchWorkspaceBoolField's edit must survive")
	assert.Contains(t, got, `backend: "codex"`, "PatchAgentStringField's edit must survive")
	assert.Contains(t, got, `analysis_mode: "manual"`, "PatchDependenciesStringField's edit must survive")
	assert.Contains(t, got, "profiles:", "PatchProfilesBlock's edit must survive")
	assert.Contains(t, got, "reviewer:")
	assert.Equal(t, 1, strings.Count(got, "\ndependencies:"), "exactly one dependencies: header")
	assertValidWorkflowYAML(t, got)
}

// TestPatchWritersDoNotLoseAnUpdateUnderInterleavedReadModifyWrite is the
// deterministic-per-trial lost-update regression CORE-006 needs beyond
// TestConcurrentPatchAndProfileSaveNoLostUpdate and
// TestPatchDependenciesStringFieldConcurrent above. Those two stress tests
// use idempotent SET operations repeated across many rounds on one shared
// file: if an early round's write is lost, a LATER round setting the same
// key to the same target value "heals" the file before the final
// assertion — the test still passes with or without the per-path lock for
// the lost-update failure mode specifically (it remains valid, real
// coverage for a DIFFERENT defect: a torn write, a duplicate header, or
// invalid YAML, which is what its `assertValidWorkflowYAML` and
// `dependencies:` header-count assertions catch — see CORE-125).
//
// This test instead runs many independent TRIALS, each on a FRESH file,
// each racing exactly ONE write by the row's writer against exactly one
// write of a DIFFERENT key by a partner writer. Because each key is written
// exactly once per trial, there is no later round to heal a lost update —
// if either write is clobbered by the other's atomic rename, that trial's
// assertion fails immediately. It is table-driven over EVERY WORKFLOW.md
// writer that serializes through lockForPath (one row per distinct writer
// function, M0-close G9): the previous version covered only the
// PatchAgentBoolField/PatchDependenciesStringField pair, so e.g.
// PatchIntField's own lockForPath call could be deleted with the whole
// package suite still green.
//
// Forcing the two goroutines' read-modify-write windows to overlap
// DETERMINISTICALLY would require an unexported test hook inside the
// Patch* call path (e.g. a pause point between the read and the write) —
// there is none, and this test intentionally does not add one to
// production code. Within a single trial the race window is only a
// handful of syscalls wide, so interleaving is not guaranteed on any one
// trial; running many independent trials makes the cumulative probability
// of observing at least one lost update overwhelming when the lock is
// absent, while the locked implementation is unconditionally correct on
// every trial, every run — not merely likely. See rounds.md for the
// quoted FAIL from reverting `lockForPath` to a no-op and re-running this
// exact test.
func TestPatchWritersDoNotLoseAnUpdateUnderInterleavedReadModifyWrite(t *testing.T) {
	const fixture = "---\ntracker:\n  kind: linear\n  active_states: [\"Todo\"]\n  terminal_states: [\"Done\"]\n" +
		"agent:\n  command: claude\n  max_concurrent_agents: 1\nworkspace:\n  root: /tmp/ws\n---\n\nBody.\n"

	type writer struct {
		write func(path string) error
		want  []string // substrings the file must contain after this write
	}
	deps := writer{
		func(p string) error { return workflow.PatchDependenciesStringField(p, "analysis_mode", "manual") },
		[]string{`analysis_mode: "manual"`},
	}
	boolW := writer{
		func(p string) error { return workflow.PatchAgentBoolField(p, "verbose", true) },
		[]string{"verbose: true"},
	}
	rows := []struct {
		name    string
		w       writer
		partner writer
	}{
		{"PatchIntField", writer{
			func(p string) error { return workflow.PatchIntField(p, "max_concurrent_agents", 7) },
			[]string{"max_concurrent_agents: 7"}}, deps},
		{"PatchAgentBoolField", boolW, deps},
		{"PatchWorkspaceBoolField", writer{
			func(p string) error { return workflow.PatchWorkspaceBoolField(p, "auto_clear", true) },
			[]string{"auto_clear: true"}}, deps},
		{"PatchAgentStringField", writer{
			func(p string) error { return workflow.PatchAgentStringField(p, "backend", "codex") },
			[]string{`backend: "codex"`}}, deps},
		{"PatchDependenciesStringField", deps, boolW},
		{"PatchAgentStringSliceField", writer{
			func(p string) error {
				return workflow.PatchAgentStringSliceField(p, "ssh_hosts", []string{"slice-host-g9"})
			},
			[]string{"slice-host-g9"}}, deps},
		{"PatchAgentStringMapField", writer{
			func(p string) error {
				return workflow.PatchAgentStringMapField(p, "ssh_host_descriptions", map[string]string{"h1": "map-desc-g9"})
			},
			[]string{"map-desc-g9"}}, deps},
		{"PatchProfilesBlock", writer{
			func(p string) error {
				return workflow.PatchProfilesBlock(p, map[string]workflow.ProfileEntry{"profile-g9": {Command: "claude --model opus"}})
			},
			[]string{"profile-g9:"}}, deps},
		{"PatchAutomationsBlock", writer{
			func(p string) error {
				return workflow.PatchAutomationsBlock(p, []workflow.AutomationEntry{{
					ID: "automation-g9", Enabled: true, Profile: "reviewer",
					Trigger: workflow.AutomationTriggerEntry{Type: "cron", Cron: "0 9 * * 1-5"},
				}})
			},
			[]string{"automation-g9"}}, deps},
		{"PatchReviewerConfig", writer{
			func(p string) error { return workflow.PatchReviewerConfig(p, "reviewer-g9", true) },
			[]string{"reviewer-g9", "auto_review: true"}}, deps},
		{"PatchTrackerStates", writer{
			func(p string) error {
				return workflow.PatchTrackerStates(p, []string{"Todo", "Active-G9"}, []string{"Done"}, "Done-G9")
			},
			[]string{"Active-G9", "Done-G9"}}, deps},
		{"PatchAgentMaxRetries", writer{
			func(p string) error { return workflow.PatchAgentMaxRetries(p, 4) },
			[]string{"max_retries: 4"}}, deps},
		{"PatchTrackerFailedState", writer{
			func(p string) error { return workflow.PatchTrackerFailedState(p, "Failed-G9") },
			[]string{"Failed-G9"}}, deps},
		{"Doc.Save", writer{
			func(p string) error { return workflow.NewDoc(p).SetAgentString("model", "doc-model-g9").Save() },
			[]string{"doc-model-g9"}}, deps},
	}

	// 50 trials per row: each trial costs two fsync'd atomic writes, and a
	// no-op lockForPath loses an update within the first few trials of every
	// row (see rounds.md), so 50 keeps the falsification overwhelming while
	// holding the -count=5 gate to a reasonable wall time.
	const trials = 50
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			lost := 0
			dir := t.TempDir()
			for i := 0; i < trials; i++ {
				// A fresh file per trial (one TempDir per row keeps the
				// per-trial cost to a single file write).
				f := filepath.Join(dir, fmt.Sprintf("WORKFLOW-%d.md", i))
				require.NoError(t, os.WriteFile(f, []byte(fixture), 0o644))

				var wg sync.WaitGroup
				errs := make([]error, 2)
				// start synchronizes both goroutines' launch as tightly as the
				// Go scheduler allows, maximizing the chance their
				// read-modify-write windows overlap within this trial.
				start := make(chan struct{})
				for j, w := range []writer{row.w, row.partner} {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						errs[j] = w.write(f)
					}()
				}
				close(start)
				wg.Wait()
				require.NoError(t, errs[0], "trial %d: %s", i, row.name)
				require.NoError(t, errs[1], "trial %d: partner of %s", i, row.name)

				data, err := os.ReadFile(f)
				require.NoError(t, err)
				got := string(data)
				for _, want := range append(append([]string{}, row.w.want...), row.partner.want...) {
					if !strings.Contains(got, want) {
						lost++
						t.Errorf("trial %d: %q lost — one write clobbered the other's (row %s)\n%s", i, want, row.name, got)
					}
				}
				assertValidWorkflowYAML(t, got)
				if lost > 0 {
					return // one quoted loss is proof enough; keep the output short
				}
			}
		})
	}
}

// TestPatchDependenciesStringFieldConcurrent mirrors TestPatchIntFieldConcurrent:
// >=100 rounds racing the (formerly unlocked) dependencies writer against
// PatchIntField and PatchAgentBoolField (both already locked before this
// batch) on one file. All three edits must survive every round.
//
// NOTE (M0-close G9): this is a corruption/duplicate-header stress test, not
// the lost-update coverage. Every write is an idempotent SET repeated across
// rounds on one shared file, so a write lost in any round but the last is
// healed by a later round before the final assertion; only a loss in the
// final overlapping round is observable, which makes its lost-update signal
// incidental (it does fail with a no-op lockForPath today, but nothing
// guarantees it). Per-writer, one-write-per-key lost-update coverage is
// TestPatchWritersDoNotLoseAnUpdateUnderInterleavedReadModifyWrite.
func TestPatchDependenciesStringFieldConcurrent(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  max_concurrent_agents: 1\n  verbose: false\n---\n\nBody.\n")

	const rounds = 100
	var wg sync.WaitGroup
	errCh := make(chan error, rounds*3)
	for i := 0; i < rounds; i++ {
		wg.Add(3)
		go func(n int) {
			defer wg.Done()
			if err := workflow.PatchIntField(f, "max_concurrent_agents", n); err != nil {
				errCh <- err
			}
		}(i + 1)
		go func() {
			defer wg.Done()
			if err := workflow.PatchAgentBoolField(f, "verbose", true); err != nil {
				errCh <- err
			}
		}()
		go func() {
			defer wg.Done()
			if err := workflow.PatchDependenciesStringField(f, "analysis_mode", "manual"); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "verbose: true", "PatchAgentBoolField's edit must survive")
	assert.Contains(t, got, `analysis_mode: "manual"`, "PatchDependenciesStringField's edit must survive")
	assert.Equal(t, 1, strings.Count(got, "\ndependencies:"), "exactly one dependencies: header")
	assertValidWorkflowYAML(t, got)

	cfg, err := config.Load(f)
	require.NoError(t, err)
	assert.Equal(t, config.DepsAnalysisModeManual, cfg.Dependencies.AnalysisMode, "the patched file must round-trip through config.Load")
}

func TestPatchProfilesBlock_Create(t *testing.T) {
	// File with no profiles block — adds one under agent:
	content := "---\nagent:\n  max_concurrent_agents: 3\n  command: claude\n---\n\nPrompt body.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	disabled := false
	profiles := map[string]workflow.ProfileEntry{
		"fast":     {Command: "claude --model claude-haiku-4-5-20251001", AllowedActions: []string{"comment", "provide_input"}},
		"thorough": {Command: "claude --model claude-opus-4-6"},
		"codex":    {Command: "run-codex-wrapper", Backend: "codex", Enabled: &disabled},
		"triage":   {Command: "claude --model claude-sonnet-4-6", AllowedActions: []string{"create_issue"}, CreateIssueState: "Todo"},
	}
	require.NoError(t, workflow.PatchProfilesBlock(f, profiles))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "  profiles:")
	assert.Contains(t, got, "    fast:")
	assert.Contains(t, got, "      command: claude --model claude-haiku-4-5-20251001")
	assert.Contains(t, got, "      allowed_actions:")
	assert.Contains(t, got, "        - comment")
	assert.Contains(t, got, "        - provide_input")
	assert.Contains(t, got, "    codex:")
	assert.Contains(t, got, "      command: run-codex-wrapper")
	assert.Contains(t, got, "      backend: codex")
	assert.Contains(t, got, "      enabled: false")
	assert.Contains(t, got, "    triage:")
	assert.Contains(t, got, "      create_issue_state: \"Todo\"")
	assert.Contains(t, got, "    thorough:")
	assert.Contains(t, got, "      command: claude --model claude-opus-4-6")
	assert.NotContains(t, got, "    fast:\n      enabled: false")
	// Other fields preserved
	assert.Contains(t, got, "max_concurrent_agents: 3")
	assert.Contains(t, got, "command: claude")
	// Body preserved
	assert.Contains(t, got, "Prompt body.")
}

func TestPatchProfilesBlock_Replace(t *testing.T) {
	// File with existing profiles — replaces them.
	content := "---\nagent:\n  max_concurrent_agents: 5\n  profiles:\n    old:\n      command: claude --model old\n---\n\nBody.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	disabled := false
	profiles := map[string]workflow.ProfileEntry{
		"fast": {
			Command:          "run-codex-wrapper",
			Backend:          "codex",
			Enabled:          &disabled,
			AllowedActions:   []string{"move_state", "create_issue"},
			CreateIssueState: "Todo",
		},
	}
	require.NoError(t, workflow.PatchProfilesBlock(f, profiles))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "    fast:")
	assert.Contains(t, got, "      command: run-codex-wrapper")
	assert.Contains(t, got, "      backend: codex")
	assert.Contains(t, got, "      enabled: false")
	assert.Contains(t, got, "      allowed_actions:")
	assert.Contains(t, got, "        - create_issue")
	assert.Contains(t, got, "        - move_state")
	assert.Contains(t, got, "      create_issue_state: \"Todo\"")
	// Old profile gone
	assert.NotContains(t, got, "old:")
	// Other fields preserved
	assert.Contains(t, got, "max_concurrent_agents: 5")
	assert.Contains(t, got, "Body.")
}

// TestPatchProfilesBlock_Replace4SpaceNoDuplicate is the regression test for the
// dashboard "add profile" corruption. A 4-space-indented agent block has an
// existing profiles: block. The pre-fix MutateProfilesBlock matched only the
// literal "  profiles:" (2-space), so on a 4-space file it failed to find the
// block and INSERTED a second one at 2-space — a duplicate `profiles:` key at
// inconsistent indent that breaks YAML parsing and freezes the daemon on reload
// (see cmd/itervox/adapter_profiles.go::UpsertProfile).
func TestPatchProfilesBlock_Replace4SpaceNoDuplicate(t *testing.T) {
	content := "---\n" +
		"agent:\n" +
		"    command: claude\n" +
		"    max_concurrent_agents: 3\n" +
		"    profiles:\n" +
		"        old:\n" +
		"            command: claude --model old\n" +
		"            soul_file: .itervox/agents/old/SOUL.md\n" +
		"server:\n" +
		"    port: 8090\n" +
		"---\n\nBody.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	profiles := map[string]workflow.ProfileEntry{
		"reviewer": {Command: "claude", SoulFile: ".itervox/agents/reviewer/SOUL.md"},
		"qa":       {Command: "claude", AllowedActions: []string{"comment"}},
	}
	require.NoError(t, workflow.PatchProfilesBlock(f, profiles))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)

	// Must stay valid YAML (the bug produced a duplicate key / bad indent).
	assertValidWorkflowYAML(t, got)
	// Exactly one profiles: block — no duplicate.
	assert.Equal(t, 1, strings.Count(got, "profiles:"), "exactly one profiles block, got:\n%s", got)
	// Replacement emitted at the file's own 4-space indentation.
	assert.Contains(t, got, "    profiles:")
	assert.Contains(t, got, "        qa:")
	assert.Contains(t, got, "            command: claude")
	assert.Contains(t, got, "            allowed_actions:")
	assert.Contains(t, got, "                - comment")
	// Old profile replaced; sibling top-level keys preserved.
	assert.NotContains(t, got, "old:")
	assert.Contains(t, got, "    port: 8090")
	// And it loads cleanly with the new profiles present.
	wf, err := workflow.Load(f)
	require.NoError(t, err)
	agent, ok := wf.Config["agent"].(map[string]any)
	require.True(t, ok, "agent should be a map")
	profs, ok := agent["profiles"].(map[string]any)
	require.True(t, ok, "agent.profiles should be a map")
	assert.Contains(t, profs, "qa")
	assert.Contains(t, profs, "reviewer")
	assert.NotContains(t, profs, "old")
}

func TestPatchProfilesBlock_QuotesCreateIssueState(t *testing.T) {
	content := "---\nagent:\n  command: claude\n---\n\nBody.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	profiles := map[string]workflow.ProfileEntry{
		"triage": {
			Command:          "claude --model claude-sonnet-4-6",
			AllowedActions:   []string{"create_issue"},
			CreateIssueState: "Todo: needs clarification #1",
		},
	}
	require.NoError(t, workflow.PatchProfilesBlock(f, profiles))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	assert.Contains(t, string(data), "      create_issue_state: \"Todo: needs clarification #1\"")
}

func TestPatchProfilesBlock_Delete(t *testing.T) {
	// Passing nil profiles removes the block.
	content := "---\nagent:\n  max_concurrent_agents: 2\n  profiles:\n    fast:\n      command: claude --model fast\n---\n\nBody.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	require.NoError(t, workflow.PatchProfilesBlock(f, nil))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.NotContains(t, got, "profiles:")
	assert.NotContains(t, got, "fast:")
	// Other fields preserved
	assert.Contains(t, got, "max_concurrent_agents: 2")
	assert.Contains(t, got, "Body.")
}

func TestPatchAutomationsBlock_Create(t *testing.T) {
	content := "---\nagent:\n  command: claude\n---\n\nPrompt body.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	automations := []workflow.AutomationEntry{
		{
			ID:           "weekday-review",
			Enabled:      true,
			Profile:      "reviewer",
			Instructions: "Review backlog issues and comment with missing details.",
			Trigger: workflow.AutomationTriggerEntry{
				Type:     "cron",
				Cron:     "0 9 * * 1-5",
				Timezone: "Asia/Jerusalem",
			},
			Filter: workflow.AutomationFilterEntry{
				MatchMode:       "any",
				States:          []string{"Backlog", "Todo"},
				LabelsAny:       []string{"bug"},
				IdentifierRegex: "^ENG-",
				Limit:           2,
			},
		},
		{
			ID:           "qa-state-entry",
			Enabled:      true,
			Profile:      "qa",
			Instructions: "Run QA when the issue enters Ready for QA.",
			Trigger: workflow.AutomationTriggerEntry{
				Type:  "issue_entered_state",
				State: "Ready for QA",
			},
		},
		{
			ID:           "input-responder",
			Enabled:      true,
			Profile:      "input-responder",
			Instructions: "Answer narrow blocked-run questions.",
			Trigger: workflow.AutomationTriggerEntry{
				Type: "input_required",
			},
			Filter: workflow.AutomationFilterEntry{
				InputContextRegex: "continue|branch",
				MaxAgeMinutes:     30,
			},
			Policy: workflow.AutomationPolicyEntry{
				AutoResume: true,
			},
		},
		{
			ID:      "rate-limit-switch",
			Enabled: true,
			Profile: "fallback-codex",
			Trigger: workflow.AutomationTriggerEntry{
				Type: "rate_limited",
			},
			Policy: workflow.AutomationPolicyEntry{
				AutoResume:      true,
				SwitchToProfile: "fallback-codex",
				SwitchToBackend: "codex",
				CooldownMinutes: 45,
			},
		},
	}
	require.NoError(t, workflow.PatchAutomationsBlock(f, automations))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "automations:")
	assert.Contains(t, got, `type: cron`)
	assert.Contains(t, got, `cron: "0 9 * * 1-5"`)
	assert.Contains(t, got, `timezone: "Asia/Jerusalem"`)
	assert.Contains(t, got, "profile: reviewer")
	assert.Contains(t, got, `instructions: "Review backlog issues and comment with missing details."`)
	assert.Contains(t, got, `match_mode: "any"`)
	assert.Contains(t, got, `states: ["Backlog","Todo"]`)
	assert.Contains(t, got, `labels_any: ["bug"]`)
	assert.Contains(t, got, `identifier_regex: "^ENG-"`)
	assert.Contains(t, got, "limit: 2")
	assert.Contains(t, got, `type: issue_entered_state`)
	assert.Contains(t, got, `state: "Ready for QA"`)
	assert.Contains(t, got, `type: input_required`)
	assert.Contains(t, got, `input_context_regex: "continue|branch"`)
	assert.Contains(t, got, `max_age_minutes: 30`)
	assert.Contains(t, got, `auto_resume: true`)
	assert.Contains(t, got, `type: rate_limited`)
	assert.Contains(t, got, `auto_switch: true`)
	assert.Contains(t, got, `switch_to_profile: "fallback-codex"`)
	assert.Contains(t, got, `switch_to_backend: "codex"`)
	assert.Contains(t, got, `cooldown_minutes: 45`)
	assert.Contains(t, got, "Prompt body.")
}

func TestPatchProfilesBlock_PreservesOtherKeys(t *testing.T) {
	// Other agent keys and comments are unchanged.
	content := "---\n# Top comment\ntracker:\n  kind: linear\nagent:\n  # agent comment\n  max_concurrent_agents: 3\n  max_turns: 60\n  profiles:\n    old:\n      command: claude\nserver:\n  port: 8090\n---\n\nPrompt.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	profiles := map[string]workflow.ProfileEntry{
		"fast": {Command: "run-codex-wrapper", Backend: "codex"},
	}
	require.NoError(t, workflow.PatchProfilesBlock(f, profiles))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	// New profile present
	assert.Contains(t, got, "    fast:")
	assert.Contains(t, got, "      command: run-codex-wrapper")
	assert.Contains(t, got, "      backend: codex")
	// Old profile gone
	assert.NotContains(t, got, "    old:")
	// Other top-level keys preserved
	assert.Contains(t, got, "tracker:")
	assert.Contains(t, got, "  kind: linear")
	assert.Contains(t, got, "server:")
	assert.Contains(t, got, "  port: 8090")
	// Comments preserved
	assert.Contains(t, got, "# Top comment")
	assert.Contains(t, got, "# agent comment")
	// Body preserved
	assert.Contains(t, got, "Prompt.")
}

// --- workflow.Error ---

func TestWorkflowErrorMessage(t *testing.T) {
	err := &workflow.Error{Code: workflow.ErrMissingFile, Path: "/some/path.md"}
	assert.Equal(t, "missing_workflow_file: /some/path.md", err.Error())
}

func TestWorkflowErrorMessageWithCause(t *testing.T) {
	inner := fmt.Errorf("inner cause")
	err := &workflow.Error{Code: workflow.ErrParseError, Path: "/w.md", Cause: inner}
	msg := err.Error()
	assert.Contains(t, msg, "workflow_parse_error")
	assert.Contains(t, msg, "/w.md")
	assert.Contains(t, msg, "inner cause")
}

func TestWorkflowErrorUnwrap(t *testing.T) {
	inner := fmt.Errorf("root")
	err := &workflow.Error{Code: workflow.ErrParseError, Path: "p", Cause: inner}
	assert.Equal(t, inner, err.Unwrap())
}

func TestWorkflowErrorUnwrapNoCause(t *testing.T) {
	err := &workflow.Error{Code: workflow.ErrMissingFile, Path: "p"}
	assert.Nil(t, err.Unwrap())
}

// --- PatchAgentBoolField ---

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))
	return f
}

func TestPatchAgentBoolFieldSetTrue(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  verbose: false\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentBoolField(f, "verbose", true))

	data, _ := os.ReadFile(f)
	assert.Contains(t, string(data), "  verbose: true")
	assert.Contains(t, string(data), "Body.")
}

func TestPatchAgentBoolFieldSetFalseRemovesKey(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  verbose: true\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentBoolField(f, "verbose", false))

	data, _ := os.ReadFile(f)
	assert.NotContains(t, string(data), "verbose")
}

func TestPatchAgentBoolFieldInsertWhenMissing(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  max_turns: 50\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentBoolField(f, "auto_resume", true))

	data, _ := os.ReadFile(f)
	assert.Contains(t, string(data), "  auto_resume: true")
}

// Toggling a bool field in a workflow whose `agent:`
// block uses 4-space indent (the default produced by yaml.v3 serialisation
// on many Go encoders) MUST preserve that indent. Before the fix, the
// patcher hardcoded 2-space indent and produced "  inline_input: true\n
// auto_review: false" — invalid YAML at line 3.
func TestPatchAgentBoolFieldPreserves4SpaceIndent(t *testing.T) {
	source := "---\nagent:\n    auto_review: false\n    max_turns: 50\n---\n\nBody.\n"
	f := writeTmp(t, source)
	require.NoError(t, workflow.PatchAgentBoolField(f, "inline_input", true))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "    inline_input: true", "new key must adopt the 4-space indent the block already uses")
	// The file must still parse as valid YAML — the actual symptom on
	// inhabited workflows was a parse error at the mismatch line. This
	// catches both an outright indent mismatch and any subtle prefix-overlap
	// regression that might survive the textual contains check.
	assertValidWorkflowYAML(t, got)
}

// Toggling off (delete) on a 4-space file must also
// preserve the file's indent for siblings.
func TestPatchAgentBoolFieldSetFalsePreserves4SpaceIndent(t *testing.T) {
	source := "---\nagent:\n    inline_input: true\n    auto_review: false\n---\n\nBody.\n"
	f := writeTmp(t, source)
	require.NoError(t, workflow.PatchAgentBoolField(f, "inline_input", false))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.NotContains(t, got, "inline_input")
	assert.Contains(t, got, "    auto_review: false", "remaining sibling must keep its original 4-space indent")
	assertValidWorkflowYAML(t, got)
}

// TestPatchWorkspaceBoolFieldWritesWorkspaceBlock pins that
// PatchWorkspaceBoolField targets the workspace: block, not agent: (CORE-006:
// there is no MutateWorkspaceBoolField export — it must call the
// package-private mutateBlockBoolField("workspace", ...) directly rather than
// MutateAgentBoolField, which is hardcoded to agent:).
func TestPatchWorkspaceBoolFieldWritesWorkspaceBlock(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  command: claude\nworkspace:\n  root: /tmp/ws\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchWorkspaceBoolField(f, "auto_clear", true))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "workspace:\n  root: /tmp/ws\n  auto_clear: true", "the key must land under workspace:, not agent:")
	assert.NotContains(t, got, "agent:\n  command: claude\n  auto_clear")
	assertValidWorkflowYAML(t, got)
}

func TestPatchAgentBoolFieldNoFrontMatterErrors(t *testing.T) {
	f := writeTmp(t, "No front matter here.\n")
	err := workflow.PatchAgentBoolField(f, "verbose", true)
	require.Error(t, err)
}

func TestPatchAgentBoolFieldMissingFileErrors(t *testing.T) {
	err := workflow.PatchAgentBoolField("/no/such/file.md", "verbose", true)
	require.Error(t, err)
}

// --- PatchAgentStringField ---

func TestPatchAgentStringFieldSet(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  backend: claude\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentStringField(f, "backend", "codex"))

	data, _ := os.ReadFile(f)
	// PatchAgentStringField stores strings quoted.
	assert.Contains(t, string(data), "backend")
	assert.Contains(t, string(data), "codex")
}

func TestPatchAgentStringFieldRemoveWhenEmpty(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  backend: codex\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentStringField(f, "backend", ""))

	data, _ := os.ReadFile(f)
	assert.NotContains(t, string(data), "backend")
}

func TestPatchAgentStringFieldInsertWhenMissing(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  max_turns: 40\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentStringField(f, "backend", "codex"))

	data, _ := os.ReadFile(f)
	assert.Contains(t, string(data), "backend")
	assert.Contains(t, string(data), "codex")
}

func TestPatchAgentStringFieldNoFrontMatterErrors(t *testing.T) {
	f := writeTmp(t, "Just body.\n")
	err := workflow.PatchAgentStringField(f, "backend", "codex")
	require.Error(t, err)
}

func TestPatchAgentStringFieldMissingFileErrors(t *testing.T) {
	err := workflow.PatchAgentStringField("/no/such/file.md", "backend", "codex")
	require.Error(t, err)
}

// --- PatchDependenciesStringField ---

func TestPatchDependenciesStringFieldCreatesBlock(t *testing.T) {
	// The scaffold never emits a dependencies: block, so the common case is
	// a file without one. The patcher must CREATE the header — appending an
	// indented key with no header would land it under the previous block.
	f := writeTmp(t, "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\nserver:\n  port: 8090\n---\nbody\n")

	require.NoError(t, workflow.PatchDependenciesStringField(f, "analysis_mode", "manual"))

	got, _ := os.ReadFile(f)
	assert.Contains(t, string(got), "dependencies:\n  analysis_mode: \"manual\"\n")
	assertValidWorkflowYAML(t, string(got))
	cfg, err := config.Load(f)
	require.NoError(t, err)
	assert.Equal(t, config.DepsAnalysisModeManual, cfg.Dependencies.AnalysisMode, "the patched file must round-trip through config.Load")
	require.NotNil(t, cfg.Server.Port, "the server block must be untouched")
	assert.Equal(t, 8090, *cfg.Server.Port, "the server block must be untouched")
}

func TestPatchDependenciesStringFieldUpdatesInPlace(t *testing.T) {
	f := writeTmp(t, "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\ndependencies:\n  analysis_mode: \"auto\"\n  stacked_prs: true\n---\nbody\n")

	require.NoError(t, workflow.PatchDependenciesStringField(f, "analysis_mode", "manual"))

	got, _ := os.ReadFile(f)
	assert.Contains(t, string(got), "  analysis_mode: \"manual\"\n")
	assert.Equal(t, 1, strings.Count(string(got), "analysis_mode:"), "updated in place, not duplicated")
	assert.Contains(t, string(got), "  stacked_prs: true\n", "sibling keys survive")
}

// TestPatchDependenciesStringFieldCommentedHeader covers CORE-125's
// header-tolerance fix: a "dependencies:" header with a trailing comment or
// trailing whitespace is valid YAML but was not byte-equal to "dependencies:",
// so the old exact-match findBlockHeader created a SECOND top-level header —
// producing a duplicate-key YAML file that failed to load. The negative
// fixture (a "dependencies:" line indented under another block) must NOT be
// matched as the top-level header — a new top-level header is created
// instead and the nested, unrelated block is left alone.
func TestPatchDependenciesStringFieldCommentedHeader(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{
			name:   "trailing comment",
			source: "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\ndependencies: # LLM analyzer\n  stacked_prs: true\n---\nbody\n",
		},
		{
			name:   "trailing whitespace",
			source: "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\ndependencies:   \n  stacked_prs: true\n---\nbody\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := writeTmp(t, tc.source)
			require.NoError(t, workflow.PatchDependenciesStringField(f, "analysis_mode", "manual"))

			got, err := os.ReadFile(f)
			require.NoError(t, err)
			body := string(got)
			assert.Equal(t, 1, strings.Count(body, "\ndependencies:"), "must not append a duplicate top-level dependencies: header")
			assert.Contains(t, body, "stacked_prs: true", "the pre-existing sibling key must survive")
			assertValidWorkflowYAML(t, body)

			cfg, err := config.Load(f)
			require.NoError(t, err, "the patched file must round-trip through config.Load")
			assert.Equal(t, config.DepsAnalysisModeManual, cfg.Dependencies.AnalysisMode)

			wf, err := workflow.Load(f)
			require.NoError(t, err, "the patched file must load through workflow.Load")
			assert.Equal(t, map[string]any{"stacked_prs": true, "analysis_mode": "manual"}, wf.Config["dependencies"])
		})
	}

	t.Run("negative: indented bare dependencies header under another block is not matched", func(t *testing.T) {
		// "  dependencies:" here is a bare nested HEADER — a child mapping
		// of "notes:", not the top-level block. This is exactly the line a
		// naive strings.TrimSpace(line) == "dependencies:" match would
		// wrongly accept (M0-close G11: the previous fixture,
		// "  dependencies: see README", carried a value, so even the
		// rejected TrimSpace matcher skipped it and the row proved
		// nothing). The patcher must create its own top-level header and
		// leave the nested block byte-for-byte alone.
		const nested = "notes:\n  dependencies:\n    doc: see README\n"
		f := writeTmp(t, "---\nitervox_schema_version: 2\n"+nested+"tracker:\n  kind: linear\n---\nbody\n")
		require.NoError(t, workflow.PatchDependenciesStringField(f, "analysis_mode", "manual"))

		got, err := os.ReadFile(f)
		require.NoError(t, err)
		body := string(got)
		assert.Equal(t, 1, strings.Count(body, "\ndependencies:"), "exactly one top-level dependencies: header must be created")
		assert.Contains(t, body, "\n"+nested, "the nested, unrelated block must be left untouched")

		wf, err := workflow.Load(f)
		require.NoError(t, err, "the patched file must load through workflow.Load")
		assert.Equal(t, map[string]any{"analysis_mode": "manual"}, wf.Config["dependencies"],
			"the edit lands in a new top-level dependencies block")
		assert.Equal(t, map[string]any{"dependencies": map[string]any{"doc": "see README"}}, wf.Config["notes"],
			"notes.dependencies is untouched — no analysis_mode injected into it")
	})
}

// TestPatchDependenciesStringFieldRefusesUnparseableResult covers CORE-125's
// write-time re-parse guard: a quoted "dependencies": key is intentionally
// NOT recognised by findBlockHeader (a stated non-goal), so the patcher would
// create a second, plain "dependencies:" header — which YAML treats as the
// SAME key as the quoted one, producing an unparseable duplicate-key file.
// The guard must refuse the write and leave the file byte-identical instead.
func TestPatchDependenciesStringFieldRefusesUnparseableResult(t *testing.T) {
	source := "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n\"dependencies\":\n  analysis_mode: \"auto\"\n---\nbody\n"
	f := writeTmp(t, source)

	err := workflow.PatchDependenciesStringField(f, "analysis_mode", "manual")
	require.Error(t, err)

	got, readErr := os.ReadFile(f)
	require.NoError(t, readErr)
	assert.Equal(t, source, string(got), "a refused write must leave the file byte-identical")
}

func TestPatchAgentStringSliceFieldInsertWhenMissing(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  command: claude\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentStringSliceField(f, "ssh_hosts", []string{"worker-1", "worker-2"}))

	data, _ := os.ReadFile(f)
	assert.Contains(t, string(data), `ssh_hosts: ["worker-1","worker-2"]`)
}

func TestPatchAgentStringMapFieldSetAndRemove(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  command: claude\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentStringMapField(f, "ssh_host_descriptions", map[string]string{
		"worker-1":      "fast box",
		"worker-2:2222": "gpu box",
	}))

	data, _ := os.ReadFile(f)
	assert.Contains(t, string(data), `ssh_host_descriptions:`)
	assert.Contains(t, string(data), `"worker-1": "fast box"`)
	assert.Contains(t, string(data), `"worker-2:2222": "gpu box"`)

	require.NoError(t, workflow.PatchAgentStringMapField(f, "ssh_host_descriptions", nil))
	data, _ = os.ReadFile(f)
	assert.NotContains(t, string(data), "ssh_host_descriptions:")
}

func TestPatchReviewerConfig_SetAndClear(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte("---\ntracker:\n  kind: linear\nagent:\n  command: claude\n---\n\nBody.\n"), 0o644))

	require.NoError(t, workflow.PatchReviewerConfig(f, "reviewer", true))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, `reviewer_profile: "reviewer"`)
	assert.Contains(t, got, `auto_review: true`)

	require.NoError(t, workflow.PatchReviewerConfig(f, "", false))

	data, err = os.ReadFile(f)
	require.NoError(t, err)
	got = string(data)
	assert.NotContains(t, got, "reviewer_profile:")
	assert.NotContains(t, got, "auto_review:")
	assert.Contains(t, got, "  command: claude")
}

func TestPatchTrackerStates_InsertsMissingKeysInsideTrackerBlock(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte("---\ntracker:\n  kind: linear\nagent:\n  command: claude\n---\n\nBody.\n"), 0o644))

	require.NoError(t, workflow.PatchTrackerStates(f, []string{"Todo"}, []string{"Done"}, "In Review"))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "tracker:\n  kind: linear\n  active_states: [\"Todo\"]\n  terminal_states: [\"Done\"]\n  completion_state: \"In Review\"")
	assert.Contains(t, got, "agent:\n  command: claude")
}

func TestPatchAgentMaxRetries_ReplaceAndInsert(t *testing.T) {
	tmp := t.TempDir()

	// Existing key — replace in place.
	f1 := filepath.Join(tmp, "with.md")
	require.NoError(t, os.WriteFile(f1, []byte("---\nagent:\n  command: claude\n  max_retries: 3\n---\nBody.\n"), 0o644))
	require.NoError(t, workflow.PatchAgentMaxRetries(f1, 7))
	data, err := os.ReadFile(f1)
	require.NoError(t, err)
	assert.Contains(t, string(data), "max_retries: 7")
	assert.NotContains(t, string(data), "max_retries: 3")

	// Missing key — insert inside agent block. Operator might not have set
	// it before; the UI should still be able to write a value.
	f2 := filepath.Join(tmp, "without.md")
	require.NoError(t, os.WriteFile(f2, []byte("---\nagent:\n  command: claude\n---\nBody.\n"), 0o644))
	require.NoError(t, workflow.PatchAgentMaxRetries(f2, 5))
	data, err = os.ReadFile(f2)
	require.NoError(t, err)
	assert.Contains(t, string(data), "max_retries: 5")
}

func TestPatchTrackerFailedState_SetAndClear(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte("---\ntracker:\n  kind: linear\nagent:\n  command: claude\n---\nBody.\n"), 0o644))

	require.NoError(t, workflow.PatchTrackerFailedState(f, "Backlog"))
	data, err := os.ReadFile(f)
	require.NoError(t, err)
	assert.Contains(t, string(data), `failed_state: "Backlog"`)

	// Empty string — operator picked "Pause (do not move)" — must remove
	// the key entirely so config.Load() reads back empty default.
	require.NoError(t, workflow.PatchTrackerFailedState(f, ""))
	data, err = os.ReadFile(f)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "failed_state:")
	assert.Contains(t, string(data), "  kind: linear") // sibling preserved
}
