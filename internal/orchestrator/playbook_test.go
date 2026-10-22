package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/playbook"
)

// enablePlaybooks turns playbooks on with the defaults Load would give and
// writes files into the fixture's playbooks directory.
func enablePlaybooks(t *testing.T, cfg *config.Config, files map[string]string) {
	t.Helper()
	cfg.Agent.Playbooks = config.PlaybooksConfig{Enabled: true, Classify: true, Dir: config.DefaultPlaybooksDir, Labels: config.DefaultPlaybookLabels}
	dir := playbook.Dir(cfg)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644))
	}
}

// legacyDigest recomputes the #186/#202 digest formula (no playbook field)
// independently, to prove a run without a playbook keeps its digest.
func legacyDigest(c promptComponents) string {
	h := sha256.New()
	writeField(h, []byte(promptDigestVersion))
	fields := [][2][]byte{{[]byte("soul"), []byte(c.soul)}, {[]byte("instructions"), []byte(c.instructions)}, {[]byte("rules"), canonicalRules(c.rules)}}
	if m := canonicalMemory(c.memory); len(m) > 0 {
		fields = append(fields, [2][]byte{[]byte("memory"), m})
	}
	fields = append(fields, [2][]byte{[]byte("template"), []byte(c.template)})
	for _, f := range fields {
		writeField(h, f[0])
		writeField(h, f[1])
	}
	return hex.EncodeToString(h.Sum(nil))
}

const bugFixPlaybook = "---\ndescription: Reproduce first.\nkeywords: [crash]\nrequire_evidence: [test]\n---\nPLAYBOOK-MARK reproduce the bug first, then fix the root cause."

// TestPlaybookInPromptAfterInstructionsAndInDigest (#218): the chosen
// playbook is a `## Playbook: <name>` block right after INSTRUCTIONS.md in
// the prompt, is a prompt-digest component only when present (a run with
// no playbook keeps its pre-#218 digest), is the same for a dispatched run
// and ComposeEvalPrompt, adds its require_evidence to the evidence block,
// and is never given to a reviewer.
func TestPlaybookInPromptAfterInstructionsAndInDigest(t *testing.T) {
	f := newProvenanceFixture(t)
	before, partsBefore := f.digestFor(t, "")

	// Playbooks on, but none matches: the digest is unchanged.
	enablePlaybooks(t, f.cfg, map[string]string{"bug-fix": bugFixPlaybook})
	same, parts := f.digestFor(t, "")
	assert.Equal(t, before, same, "a run without a playbook keeps its digest")
	assert.Equal(t, partsBefore, parts)
	assert.Empty(t, parts.Playbook)
	ruleSet := f.o.resolveRunRules("implementer", "claude")
	mem := f.o.resolveRunMemory(&RunEntry{Issue: f.issue, ProfileName: "implementer"}, ruleSet)
	assert.Equal(t, legacyDigest(profilePromptComponents(f.cfg, "implementer", false, ruleSet, mem, "")), before,
		"the digest without a playbook is the pre-#218 formula")

	// The issue is labelled bug: the run gets the bug-fix playbook.
	f.issue.Labels = []string{"bug"}
	entry := &RunEntry{Issue: f.issue, ProfileName: "implementer", Backend: "claude", StartedAt: time.Now()}
	_ = f.o.startRun(t.Context(), &f.state, entry)
	assert.Equal(t, "bug-fix", entry.Playbook)
	assert.Equal(t, playbook.SourceLabel, entry.PlaybookSource)
	require.NotEmpty(t, entry.PromptParts.Playbook, "the playbook is a digest component")
	assert.NotEqual(t, before, entry.PromptDigest)
	assert.Equal(t, partsBefore.Instructions, entry.PromptParts.Instructions)
	assert.Equal(t, partsBefore.Template, entry.PromptParts.Template)

	// ComposeEvalPrompt renders and digests the same playbook.
	composed, err := ComposeEvalPrompt(EvalPromptInput{Config: f.cfg, ProfileName: "implementer", Backend: "claude", Issue: f.issue})
	require.NoError(t, err)
	assert.Equal(t, entry.PromptDigest, composed.Digest)
	text := composed.Text
	tmpl, soul := strings.Index(text, "TEMPLATE-MARK"), strings.Index(text, "SOUL-MARK")
	instr, pb := strings.Index(text, "INSTRUCTIONS-MARK"), strings.Index(text, "## Playbook: bug-fix")
	require.True(t, tmpl >= 0 && soul > tmpl && instr > soul && pb > instr,
		"order must be template → SOUL → INSTRUCTIONS → playbook (template %d, soul %d, instructions %d, playbook %d)", tmpl, soul, instr, pb)
	assert.Contains(t, text[pb:], "PLAYBOOK-MARK")
	// The playbook's require_evidence joins the (empty) profile's.
	assert.Contains(t, text, "## Evidence (required)")
	assert.Contains(t, text, "required checks: `test`")
	assert.Contains(t, text, "run.decisions_path: `.itervox/handoff/")

	// A reviewer run of the issue gets no playbook.
	rev := &RunEntry{Issue: f.issue, ProfileName: "implementer", Backend: "claude", Kind: "reviewer", StartedAt: time.Now()}
	_ = f.o.startRun(t.Context(), &f.state, rev)
	assert.Empty(t, rev.Playbook)
	assert.Empty(t, rev.PromptParts.Playbook)

	// The history records which playbook the run had.
	f.state.Running[f.issue.ID] = entry
	f.state.Claimed[f.issue.ID] = struct{}{}
	run := f.exit(t, entry)
	assert.Equal(t, "bug-fix", run.Playbook)
	assert.Equal(t, entry.PromptParts.Playbook, run.PromptParts.Playbook)
}

// TestPlaybookOverridePerIssue (#218): the operator's override is
// event-loop state (EventSetPlaybookOverride), wins over labels, can say
// "none", is cleared by "", and survives a restart through
// playbook_overrides.json.
func TestPlaybookOverridePerIssue(t *testing.T) {
	f := newProvenanceFixture(t)
	enablePlaybooks(t, f.cfg, map[string]string{"bug-fix": bugFixPlaybook, "docs": "Write for the reader."})
	f.issue.Labels = []string{"bug"}
	ledger := filepath.Join(t.TempDir(), ".itervox", "playbook_overrides.json")
	f.o.SetPlaybookOverridesFile(ledger)
	start := func() *RunEntry {
		e := &RunEntry{Issue: f.issue, ProfileName: "implementer", Backend: "claude", StartedAt: time.Now()}
		_ = f.o.startRun(t.Context(), &f.state, e)
		return e
	}
	set := func(name string) {
		require.True(t, f.o.SetPlaybookOverride("ENG-1", name))
		f.state = f.o.handleEvent(t.Context(), f.state, <-f.o.events)
	}

	assert.Equal(t, "bug-fix", start().Playbook, "automatic: the label")

	set("docs")
	assert.Equal(t, "docs", f.state.PlaybookOverrides["ENG-1"])
	e := start()
	assert.Equal(t, "docs", e.Playbook)
	assert.Equal(t, playbook.SourceOverride, e.PlaybookSource)
	data, err := os.ReadFile(ledger)
	require.NoError(t, err)
	assert.JSONEq(t, `{"ENG-1":"docs"}`, string(data))

	// A restart loads the override back.
	reloaded := f.o.loadPlaybookOverridesFromDisk(NewState(f.cfg))
	assert.Equal(t, map[string]string{"ENG-1": "docs"}, reloaded.PlaybookOverrides)

	set(playbook.None)
	e = start()
	assert.Empty(t, e.Playbook, "none attaches no playbook despite the label")
	assert.Empty(t, e.PromptParts.Playbook)

	set("")
	_, kept := f.state.PlaybookOverrides["ENG-1"]
	assert.False(t, kept, "an empty name clears the override")
	assert.Equal(t, "bug-fix", start().Playbook, "back to automatic")

	// The snapshot carries the override map (a copy).
	set("docs")
	snap := f.state.Clone()
	snap.PlaybookOverrides["ENG-1"] = "mutated"
	assert.Equal(t, "docs", f.state.PlaybookOverrides["ENG-1"])
}
