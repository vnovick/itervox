package orchestrator

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/rules"
)

// Run provenance (#186): which prompt version produced a run, and what
// operators told it while it ran. Both end up on the run's CompletedRun.
//
// The prompt digest is computed by startRun (event loop) and stored on the
// RunEntry next to RulesHash; delivered steering messages are appended to
// the RunEntry by the EventSessionMessageDelivered handler. recordHistory
// copies both onto CompletedRun, so history is still written only by the
// event loop. Only hashes are kept, never prompt text: history stays small
// and a secret pasted into a SOUL.md or a rule does not spread into it.

// promptDigestVersion prefixes the digest input so a future change to the
// canonical form cannot collide with digests computed by this one.
const promptDigestVersion = "itervox-prompt-digest/v1"

// promptPartDigestLen is the hex length kept for each per-component digest.
// The parts only answer "which component changed between two versions", so a
// 64-bit prefix is plenty; the combined PromptDigest keeps all 256 bits.
const promptPartDigestLen = 16

// PromptDigestShortLen is the length of the short form shown in the
// dashboard and accepted (as a minimum useful prefix) by the history filter.
const PromptDigestShortLen = 12

// PromptParts are the per-component digests of a run's prompt version
// (#186): truncated SHA-256 hex of each component's canonical bytes, "" for
// a component the run did not have. Hashes only — never the text.
type PromptParts struct {
	Soul         string `json:"soul,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	Rules        string `json:"rules,omitempty"`
	Template     string `json:"template,omitempty"`
}

// promptComponents are the inputs of a run's prompt version.
type promptComponents struct {
	soul, instructions, template string
	rules                        rules.Set
}

// writeField writes a length-prefixed field: an 8-byte big-endian length,
// then the bytes. Length prefixes make the concatenation unambiguous ("ab"+"c"
// and "a"+"bc" hash differently).
func writeField(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	h.Write(n[:])
	h.Write(b)
}

// canonicalRules serializes the resolved rule set independent of layer
// order: the rules sorted by id, each as its id, severity and text (what the
// prompt's rules block carries). The layer a rule came from is left out: a
// rule moved between layers unchanged gives the run the same prompt.
func canonicalRules(set rules.Set) []byte {
	if len(set.Rules) == 0 {
		return nil
	}
	sorted := slices.Clone(set.Rules)
	slices.SortFunc(sorted, func(a, b rules.Effective) int {
		return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Severity, b.Severity), cmp.Compare(a.Text, b.Text))
	})
	var b strings.Builder
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(sorted)))
	b.Write(n[:])
	for _, r := range sorted {
		for _, f := range []string{r.ID, string(r.Severity), r.Text} {
			binary.BigEndian.PutUint64(n[:], uint64(len(f)))
			b.Write(n[:])
			b.WriteString(f)
		}
	}
	return []byte(b.String())
}

// partDigest is the short digest of one component; "" when it is empty.
func partDigest(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:promptPartDigestLen]
}

// digest returns the run's prompt-version digest (64 hex chars) and its
// per-component digests. The digest is SHA-256 over a versioned,
// length-prefixed concatenation of SOUL.md, INSTRUCTIONS.md, the canonical
// rule set and the prompt template source, in that order.
func (c promptComponents) digest() (string, PromptParts) {
	ruleBytes := canonicalRules(c.rules)
	fields := []struct {
		tag  string
		body []byte
	}{
		{"soul", []byte(c.soul)},
		{"instructions", []byte(c.instructions)},
		{"rules", ruleBytes},
		{"template", []byte(c.template)},
	}
	h := sha256.New()
	writeField(h, []byte(promptDigestVersion))
	for _, f := range fields {
		writeField(h, []byte(f.tag))
		writeField(h, f.body)
	}
	return hex.EncodeToString(h.Sum(nil)), PromptParts{
		Soul:         partDigest(fields[0].body),
		Instructions: partDigest(fields[1].body),
		Rules:        partDigest(fields[2].body),
		Template:     partDigest(fields[3].body),
	}
}

// runPromptComponents gathers what a run of entry's profile is prompted
// with, the way runWorker assembles it: the profile's SOUL.md and
// INSTRUCTIONS.md, the run's resolved rules, and the WORKFLOW.md body — or
// agent.reviewer_prompt for a reviewer run when one is configured. Event
// loop only (startRun); Profiles and ReviewerProfile are cfgMu fields.
func (o *Orchestrator) runPromptComponents(entry *RunEntry, set rules.Set) promptComponents {
	c := promptComponents{rules: set}
	if o == nil || o.cfg == nil || entry == nil {
		return c
	}
	o.cfgMu.RLock()
	defer o.cfgMu.RUnlock()
	if p, ok := o.cfg.Agent.Profiles[entry.ProfileName]; ok && entry.ProfileName != "" {
		c.soul, c.instructions = p.Soul, p.Instructions
	}
	c.template = o.cfg.PromptTemplate
	reviewer := entry.Kind == "reviewer" || isReviewerProfile(o.cfg, entry.ProfileName)
	if reviewer && o.cfg.Agent.ReviewerPrompt != "" {
		c.template = o.cfg.Agent.ReviewerPrompt
	}
	return c
}

// Steering history bounds (#186). A run keeps at most maxRunSteers
// delivered operator messages (the newest; SteersDropped counts the rest),
// each redacted and then cut to maxRunSteerBytes. With the 200-run history
// ring that bounds steering history at ~4 MiB in the worst case.
const (
	maxRunSteers     = 20
	maxRunSteerBytes = 1024
)

// RunSteer is one operator message delivered to a run (#178) as kept in its
// history (#186). Text is redacted and bounded.
type RunSteer struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
	// Delivery is SteeringLive or SteeringResume.
	Delivery string `json:"delivery"`
}

// boundUTF8 cuts s to at most max bytes on a rune boundary, ending in an
// ellipsis when it was cut.
func boundUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	cut := max - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// sanitizeSteerText redacts a delivered message and then bounds it.
func sanitizeSteerText(text string) string {
	return boundUTF8(strings.TrimSpace(logging.RedactString(text)), maxRunSteerBytes)
}

// appendRunSteers records delivered messages on the run they reached. The
// slice is replaced, never appended to in place, so a copy taken before
// (a stall exit, a snapshot) is never written through. Event loop only.
func appendRunSteers(entry *RunEntry, texts []string, delivery string, at time.Time) {
	if entry == nil || len(texts) == 0 {
		return
	}
	steers := make([]RunSteer, 0, len(entry.Steers)+len(texts))
	steers = append(steers, entry.Steers...)
	for _, t := range texts {
		steers = append(steers, RunSteer{Text: sanitizeSteerText(t), At: at, Delivery: delivery})
	}
	if n := len(steers) - maxRunSteers; n > 0 {
		entry.SteersDropped += n
		steers = steers[n:]
	}
	entry.Steers = steers
}

// runEntryByRunID returns the running entry of runID for identifier, or nil.
func runEntryByRunID(state State, identifier, runID string) *RunEntry {
	for _, r := range state.Running {
		if r != nil && r.Issue.Identifier == identifier && (runID == "" || r.RunID == runID) {
			return r
		}
	}
	return nil
}

// recordRunSteers appends a worker's delivery report to its run's steering
// history. A run that is no longer running (reconcile stopped it) keeps none
// of a late report. Event loop only.
func recordRunSteers(state State, runID string, d *SessionDelivery, at time.Time) {
	if d == nil {
		return
	}
	appendRunSteers(runEntryByRunID(state, d.Identifier, runID), d.Texts, d.Delivery, at)
}
