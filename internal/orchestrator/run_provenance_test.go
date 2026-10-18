package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/rules"
	"github.com/vnovick/itervox/internal/tracker"
)

// provenanceFixture is a daemon with one profile, a prompt template and a
// project rules file, ready to start a run of ENG-1.
type provenanceFixture struct {
	o     *Orchestrator
	cfg   *config.Config
	state State
	issue domain.Issue
	wf    string
}

const provenanceProjectRules = `{"rules": [
  {"id": "pnpm", "severity": "must", "text": "RULE-TEXT-MARK use pnpm."},
  {"id": "small", "severity": "should", "text": "Keep pull requests small."}
]}`

func newProvenanceFixture(t *testing.T) *provenanceFixture {
	t.Helper()
	wf := rulesProject(t, "", provenanceProjectRules, "")
	cfg := automationBaseCfg()
	cfg.WorkflowPath = wf
	cfg.PromptTemplate = "TEMPLATE-MARK {{ issue.identifier }}"
	cfg.Agent.Profiles["implementer"] = config.AgentProfile{Command: "claude", Soul: "SOUL-MARK", Instructions: "INSTRUCTIONS-MARK"}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	o := New(cfg, tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), nil, nil)
	return &provenanceFixture{o: o, cfg: cfg, state: NewState(cfg), issue: issue, wf: wf}
}

// start dispatches a run of the implementer profile and puts it in Running.
func (f *provenanceFixture) start(t *testing.T) *RunEntry {
	t.Helper()
	entry := &RunEntry{Issue: f.issue, ProfileName: "implementer", Backend: "claude", StartedAt: time.Now(), AgentSessionID: "sess-1"}
	_ = f.o.startRun(t.Context(), &f.state, entry)
	f.state.Running[f.issue.ID] = entry
	f.state.Claimed[f.issue.ID] = struct{}{}
	return entry
}

// exit sends the run's success exit through the event loop.
func (f *provenanceFixture) exit(t *testing.T, entry *RunEntry) CompletedRun {
	t.Helper()
	attempt := 0
	f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: f.issue.ID, RunID: entry.RunID,
		RunEntry: &RunEntry{Issue: f.issue, TerminalReason: TerminalSucceeded, RetryAttempt: &attempt},
	})
	hist := f.o.RunHistory()
	require.NotEmpty(t, hist)
	return hist[len(hist)-1]
}

// digestFor is the prompt digest startRun gives a fresh run under the
// fixture's current config and files.
func (f *provenanceFixture) digestFor(t *testing.T, kind string) (string, PromptParts) {
	t.Helper()
	entry := &RunEntry{Issue: f.issue, ProfileName: "implementer", Backend: "claude", Kind: kind, StartedAt: time.Now()}
	_ = f.o.startRun(t.Context(), &f.state, entry)
	return entry.PromptDigest, entry.PromptParts
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// TestCompletedRunRecordsPromptDigestAndRunID (#186): a run dispatched by
// startRun carries the digest of its prompt version and its run ID, and its
// exit writes both onto the CompletedRun.
func TestCompletedRunRecordsPromptDigestAndRunID(t *testing.T) {
	f := newProvenanceFixture(t)
	entry := f.start(t)
	require.NotEmpty(t, entry.RunID)
	assert.Regexp(t, hex64, entry.PromptDigest, "a full SHA-256 hex digest")
	for name, part := range map[string]string{
		"soul": entry.PromptParts.Soul, "instructions": entry.PromptParts.Instructions,
		"rules": entry.PromptParts.Rules, "template": entry.PromptParts.Template,
	} {
		assert.Len(t, part, promptPartDigestLen, "%s part digest", name)
	}

	got := f.exit(t, entry)
	assert.Equal(t, entry.RunID, got.RunID, "the completed run names its run")
	assert.Equal(t, entry.PromptDigest, got.PromptDigest, "the completed run names its prompt version")
	assert.Equal(t, entry.PromptParts, got.PromptParts)
	assert.Equal(t, "succeeded", got.Status)

	// A profile without SOUL.md has no soul part, and still a digest.
	f.cfg.Agent.Profiles["implementer"] = config.AgentProfile{Command: "claude", Instructions: "INSTRUCTIONS-MARK"}
	d, parts := f.digestFor(t, "")
	assert.Regexp(t, hex64, d)
	assert.Empty(t, parts.Soul)
}

// TestPromptDigestChangesWhenRulesOrInstructionsChange (#186): the digest is
// stable while nothing changes, and changes (with the matching part) when
// SOUL.md, INSTRUCTIONS.md, the rules or the template change. A reviewer run
// is digested with the reviewer prompt it is given.
func TestPromptDigestChangesWhenRulesOrInstructionsChange(t *testing.T) {
	f := newProvenanceFixture(t)
	base, baseParts := f.digestFor(t, "")
	again, againParts := f.digestFor(t, "")
	require.Equal(t, base, again, "stable when nothing changes")
	require.Equal(t, baseParts, againParts)

	changed := func(what string, mutate func(), restore func(), part func(PromptParts) string) {
		t.Helper()
		mutate()
		d, p := f.digestFor(t, "")
		assert.NotEqual(t, base, d, "%s change must change the digest", what)
		assert.NotEqual(t, part(baseParts), part(p), "%s part must change", what)
		restore()
		d, _ = f.digestFor(t, "")
		assert.Equal(t, base, d, "restoring %s restores the digest", what)
	}
	profile := f.cfg.Agent.Profiles["implementer"]
	setProfile := func(p config.AgentProfile) func() {
		return func() { f.cfg.Agent.Profiles["implementer"] = p }
	}
	edited := profile
	edited.Instructions += "\nOne more instruction."
	changed("instructions", setProfile(edited), setProfile(profile), func(p PromptParts) string { return p.Instructions })
	edited = profile
	edited.Soul = "Another soul."
	changed("soul", setProfile(edited), setProfile(profile), func(p PromptParts) string { return p.Soul })
	changed("template", func() { f.cfg.PromptTemplate += "!" }, func() { f.cfg.PromptTemplate = strings.TrimSuffix(f.cfg.PromptTemplate, "!") },
		func(p PromptParts) string { return p.Template })
	local := rules.LocalPath(f.wf)
	changed("rules", func() {
		require.NoError(t, os.WriteFile(local, []byte(`{"rules":[{"id":"fresh","severity":"should","text":"Added."}]}`), 0o644))
	}, func() { require.NoError(t, os.Remove(local)) }, func(p PromptParts) string { return p.Rules })

	// The rules are canonical: the same rules from a reordered file hash
	// the same.
	require.NoError(t, os.WriteFile(rules.ProjectPath(f.wf), []byte(`{"rules": [
  {"id": "small", "severity": "should", "text": "Keep pull requests small."},
  {"id": "pnpm", "severity": "must", "text": "RULE-TEXT-MARK use pnpm."}
]}`), 0o644))
	d, _ := f.digestFor(t, "")
	assert.Equal(t, base, d, "rule order in the file does not change the prompt version")

	// Length-prefixed: moving bytes between components changes the digest,
	// even across a component's tag (without the prefixes both of these
	// would hash "soul x instructions y instructions").
	a, _ := promptComponents{soul: "xinstructionsy", instructions: ""}.digest()
	b, _ := promptComponents{soul: "x", instructions: "yinstructions"}.digest()
	assert.NotEqual(t, a, b)

	// A reviewer is prompted with agent.reviewer_prompt.
	f.cfg.Agent.ReviewerPrompt = "REVIEW {{ issue.identifier }}"
	rev, revParts := f.digestFor(t, "reviewer")
	assert.NotEqual(t, base, rev)
	assert.NotEqual(t, baseParts.Template, revParts.Template)
	worker, _ := f.digestFor(t, "")
	assert.Equal(t, base, worker, "a worker still gets the WORKFLOW.md body")
}

// TestHistoryNeverStoresPromptText (#186): the history entry and the history
// file carry the digest, never the SOUL.md, INSTRUCTIONS.md, rule or
// template text, so a secret in a profile does not spread into history.
func TestHistoryNeverStoresPromptText(t *testing.T) {
	f := newProvenanceFixture(t)
	secret := "ghp_" + strings.Repeat("Q9w8E7r6", 5)
	f.cfg.Agent.Profiles["implementer"] = config.AgentProfile{
		Command: "claude", Soul: "SOUL-MARK " + secret, Instructions: "INSTRUCTIONS-MARK",
	}
	historyFile := filepath.Join(t.TempDir(), "history.json")
	f.o.SetHistoryFile(historyFile)

	entry := f.start(t)
	got := f.exit(t, entry)
	require.NotEmpty(t, got.PromptDigest)

	inMemory, err := json.Marshal(got)
	require.NoError(t, err)
	onDisk, err := os.ReadFile(historyFile)
	require.NoError(t, err)
	require.Contains(t, string(onDisk), got.PromptDigest, "the file carries the digest")
	for _, blob := range []string{string(inMemory), string(onDisk)} {
		for _, text := range []string{"SOUL-MARK", "INSTRUCTIONS-MARK", "RULE-TEXT-MARK", "TEMPLATE-MARK", secret} {
			assert.NotContains(t, blob, text)
		}
	}
}

// TestDeliveredSteersAreRecordedRedacted (#186): each operator message a
// worker reports delivered — live or as a resumed turn's prompt — is kept on
// the run's history entry with its time and delivery path, redacted.
func TestDeliveredSteersAreRecordedRedacted(t *testing.T) {
	f := newProvenanceFixture(t)
	f.o.events = make(chan OrchestratorEvent, 8)
	entry := f.start(t)
	ctx := t.Context()
	h := entry.run
	secret := "ghp_" + strings.Repeat("A1b2C3d4", 5)

	// The worker side: what reportSessionDelivery sends is what the loop
	// records.
	workerCtx := context.WithValue(ctx, runHandleKey{}, h)
	f.o.reportSessionDelivery(workerCtx, "ENG-1", []agent.SteerMessage{{ID: "m1", Text: "use the staging db, token " + secret}}, SteeringLive)
	f.o.reportSessionDelivery(workerCtx, "ENG-1", []agent.SteerMessage{{ID: "m2", Text: "  then stop  "}}, SteeringResume)
	for range 2 {
		ev := <-f.o.events
		require.Equal(t, EventSessionMessageDelivered, ev.Type)
		f.state = f.o.handleEvent(ctx, f.state, ev)
	}
	// A report for another run of the issue is not this run's history.
	f.state = f.o.handleEvent(ctx, f.state, OrchestratorEvent{
		Type: EventSessionMessageDelivered, RunID: "run-other",
		SessionDelivery: &SessionDelivery{Identifier: "ENG-1", IDs: []string{"x"}, Texts: []string{"not mine"}, Delivery: SteeringLive},
	})

	got := f.exit(t, entry)
	require.Len(t, got.Steers, 2)
	assert.Equal(t, SteeringLive, got.Steers[0].Delivery)
	assert.Equal(t, SteeringResume, got.Steers[1].Delivery)
	assert.Contains(t, got.Steers[0].Text, "use the staging db")
	assert.NotContains(t, got.Steers[0].Text, secret, "the steer is redacted")
	assert.Equal(t, "then stop", got.Steers[1].Text)
	for _, s := range got.Steers {
		assert.False(t, s.At.IsZero(), "each steer has its delivery time")
	}
	blob, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(blob), secret)
}

// TestSteerHistoryIsBounded (#186): a run keeps at most maxRunSteers
// delivered messages (the newest, counting the rest), each cut to
// maxRunSteerBytes on a rune boundary.
func TestSteerHistoryIsBounded(t *testing.T) {
	entry := &RunEntry{}
	at := time.Now()
	for i := range maxRunSteers + 5 {
		appendRunSteers(entry, []string{"msg-" + string(rune('a'+i))}, SteeringResume, at)
	}
	require.Len(t, entry.Steers, maxRunSteers)
	assert.Equal(t, 5, entry.SteersDropped)
	assert.Equal(t, "msg-"+string(rune('a'+5)), entry.Steers[0].Text, "the oldest are let go")
	assert.Equal(t, "msg-"+string(rune('a'+maxRunSteers+4)), entry.Steers[maxRunSteers-1].Text)

	long := strings.Repeat("é", maxRunSteerBytes) // 2 bytes a rune
	appendRunSteers(entry, []string{long}, SteeringLive, at)
	last := entry.Steers[len(entry.Steers)-1].Text
	assert.LessOrEqual(t, len(last), maxRunSteerBytes)
	assert.True(t, utf8.ValidString(last))
	assert.True(t, strings.HasSuffix(last, "…"))

	// The slice is replaced, not appended in place: a snapshot copy taken
	// before an append is unaffected.
	before := entry.Steers
	appendRunSteers(entry, []string{"next"}, SteeringLive, at)
	assert.Equal(t, last, before[len(before)-1].Text)
}

// TestStalledRunHistoryKeepsPromptDigestAndSteers (#186): stall detection
// deletes the Running entry before the exit arrives; the stall exit it
// sends still carries the run's prompt version and steering history.
func TestStalledRunHistoryKeepsPromptDigestAndSteers(t *testing.T) {
	f := newProvenanceFixture(t)
	f.cfg.Agent.StallTimeoutMs = 1
	entry := f.start(t)
	appendRunSteers(entry, []string{"hurry up"}, SteeringResume, time.Now())
	entry.StartedAt = time.Now().Add(-time.Minute)
	events := make(chan OrchestratorEvent, 4)
	f.state = ReconcileStalls(f.state, f.cfg, time.Now(), events, nil)
	ev := <-events
	f.state = f.o.handleEvent(t.Context(), f.state, ev)
	hist := f.o.RunHistory()
	require.NotEmpty(t, hist)
	got := hist[len(hist)-1]
	assert.Equal(t, "stalled", got.Status)
	assert.Equal(t, entry.RunID, got.RunID)
	assert.Equal(t, entry.PromptDigest, got.PromptDigest)
	assert.Equal(t, entry.RulesHash, got.RulesHash)
	require.Len(t, got.Steers, 1)
	assert.Equal(t, "hurry up", got.Steers[0].Text)
}

// TestHistoryFileWithoutProvenanceStillLoads (#186): history written before
// these fields existed loads unchanged, and a run without them writes none
// of the new keys.
func TestHistoryFileWithoutProvenanceStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	legacy := `[{"Identifier":"ENG-9","Status":"succeeded","StartedAt":"2026-01-01T00:00:00Z","FinishedAt":"2026-01-01T00:01:00Z","profileName":"implementer","rulesHash":"sha256:abc"}]`
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o644))
	o := New(automationBaseCfg(), nil, nil, nil)
	o.SetHistoryFile(path)
	o.loadHistoryFromDisk()
	hist := o.RunHistory()
	require.Len(t, hist, 1)
	assert.Equal(t, "ENG-9", hist[0].Identifier)
	assert.Empty(t, hist[0].PromptDigest)
	assert.Empty(t, hist[0].Steers)

	blob, err := json.Marshal(CompletedRun{Identifier: "ENG-1"})
	require.NoError(t, err)
	for _, key := range []string{"runID", "promptDigest", "promptParts", "steers", "steersDropped"} {
		assert.NotContains(t, string(blob), `"`+key+`"`, "omitted when empty")
	}
}
