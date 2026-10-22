package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/memory"
)

// Enforcement suggestions (#217). When a lesson's evidence reaches
// memory.lessons.enforce_threshold, the drafting goroutine that bumped it
// drafts the strongest enforcement it can (a lint rule, a type, a test, a
// CLAUDE.md or AGENTS.md rule, or a rules-layer entry) through the same
// budgeted call as the lesson itself, with memory.TemplateEnforcement as
// the fallback, and records it on the lesson. Accepting a suggestion (an
// operator, through the Memory page) creates an "Enforce: <lesson>" issue in
// the tracker, the way an agent's create_issue action does, carrying the
// draft and the evidence: an agent then implements it like any other issue.
// Nothing here writes code or commits.

// suggestEnforcement drafts and records an enforcement suggestion for a
// lesson whose evidence reached the threshold. Off the event loop (the
// drafting goroutine); nil when none was suggested.
func (o *Orchestrator) suggestEnforcement(ctx context.Context, store memory.Store, e memory.Entry, plan lessonDraftPlan) *memory.Enforcement {
	if !memory.NeedsEnforcement(e, plan.EnforceThreshold) {
		return nil
	}
	enf := memory.TemplateEnforcement(e)
	if plan.Command != "" {
		out, err := o.runDraftTurn(ctx, e.Source.Issue, enforcementDraftPrompt(e), plan)
		if err == nil {
			if d, ok := parseEnforcementDraft(out); ok {
				d.DraftedBy = "profile:" + plan.Profile
				enf = d
			} else {
				err = errors.New("orchestrator: enforcement drafting turn returned no usable suggestion")
			}
		}
		if err != nil {
			slog.Warn("orchestrator: enforcement drafting call failed; using the template",
				"memory_id", e.ID, "profile", plan.Profile, "error", err)
		}
	}
	got, ok, err := store.SuggestEnforcement(e.ID, enf)
	if err != nil {
		slog.Warn("orchestrator: enforcement suggestion not saved", "memory_id", e.ID, "error", err)
		return nil
	}
	if !ok || got.Lesson == nil {
		return nil
	}
	return got.Lesson.Enforcement
}

// enforcementDraftPrompt is the enforcement drafting turn's prompt: the
// lesson, its evidence and the output contract.
func enforcementDraftPrompt(e memory.Entry) string {
	var b strings.Builder
	b.WriteString("An AI coding agent keeps making the same mistake; the lesson below has been learned several times. ")
	b.WriteString("Suggest the STRONGEST enforcement that would catch it automatically, preferring in order: a lint rule, a type, a test; ")
	b.WriteString("then a CLAUDE.md or AGENTS.md rule (\"docs\"), and an Itervox rule (\"rule\") only when nothing can check it.\n")
	b.WriteString("Do not run tools, read files or change anything. Reply with ONLY a JSON object, no prose and no code fence:\n")
	b.WriteString(`{"kind": "lint|type|test|docs|rule", "title": "...", "draft": "..."}` + "\n")
	b.WriteString("title: at most 100 characters. draft: what to add and where, concrete enough for another agent to implement, at most 1500 characters, no secrets.\n\n")
	fmt.Fprintf(&b, "Lesson: %s\n", boundUTF8(strings.Join(strings.Fields(e.Text), " "), lessonEvidenceBytes))
	if l := e.Lesson; l != nil {
		fmt.Fprintf(&b, "Trigger: %s, seen %d times\n", l.Trigger, l.Evidence)
		if l.Situation != "" {
			fmt.Fprintf(&b, "Applies when: %s\n", boundUTF8(l.Situation, lessonEvidenceBytes))
		}
		if l.Correction != "" {
			fmt.Fprintf(&b, "In the human's words: %s\n", boundUTF8(strings.Join(strings.Fields(l.Correction), " "), lessonEvidenceBytes))
		}
	}
	return b.String()
}

// parseEnforcementDraft reads the enforcement drafting turn's output: a
// JSON object with a known kind and a draft.
func parseEnforcementDraft(out string) (memory.Enforcement, bool) {
	i, j := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if i < 0 || j <= i {
		return memory.Enforcement{}, false
	}
	var d struct {
		Kind  string `json:"kind"`
		Title string `json:"title"`
		Draft string `json:"draft"`
	}
	if json.Unmarshal([]byte(out[i:j+1]), &d) != nil {
		return memory.Enforcement{}, false
	}
	kind := memory.EnforcementKind(strings.ToLower(strings.TrimSpace(d.Kind)))
	draft := strings.TrimSpace(d.Draft)
	if !slices.Contains(memory.EnforcementKinds, kind) || draft == "" {
		return memory.Enforcement{}, false
	}
	return memory.Enforcement{
		Kind: kind, Status: memory.EnforcementSuggested,
		Title: boundUTF8(strings.TrimSpace(d.Title), 200), Draft: boundUTF8(draft, memory.MaxTextBytes),
	}, true
}

// enforceIssueState is the tracker state an "Enforce:" issue is created in:
// memory.lessons.enforce_issue_state, else the first backlog state, else the
// first active state (read under cfgMu).
func (o *Orchestrator) enforceIssueState() string {
	if s := strings.TrimSpace(o.cfg.Memory.Lessons.EnforceIssueState); s != "" {
		return s
	}
	if len(o.cfg.Tracker.BacklogStates) > 0 {
		return o.cfg.Tracker.BacklogStates[0]
	}
	active, _, _ := o.TrackerStatesCfg()
	if len(active) > 0 {
		return active[0]
	}
	return ""
}

// AcceptLessonEnforcement accepts the lesson's pending enforcement
// suggestion: it creates the "Enforce: <lesson>" issue in the tracker (in
// the lesson's source issue's project, as an agent's create_issue action
// does) and records the issue on the lesson. memory.ErrConflict when there
// is no pending suggestion. Called from an HTTP handler goroutine: it reads
// the memory files and calls the tracker, never State.
func (o *Orchestrator) AcceptLessonEnforcement(ctx context.Context, id string) (memory.Entry, *domain.Issue, error) {
	store := memory.Store{WorkflowPath: o.cfg.WorkflowPath}
	e, err := store.Get(id)
	if err != nil {
		return memory.Entry{}, nil, err
	}
	if e.Lesson == nil || e.Lesson.Enforcement == nil || e.Lesson.Enforcement.Status != memory.EnforcementSuggested {
		return memory.Entry{}, nil, fmt.Errorf("%w: %q has no pending enforcement suggestion", memory.ErrConflict, id)
	}
	if o.tracker == nil {
		return memory.Entry{}, nil, errors.New("orchestrator: no tracker to create the enforcement issue in")
	}
	sourceID := ""
	if e.Source.Issue != "" {
		if src, err := o.tracker.FetchIssueByIdentifier(ctx, e.Source.Issue); err == nil && src != nil {
			sourceID = src.ID
		}
	}
	created, err := o.tracker.CreateIssue(ctx, sourceID, memory.EnforcementIssueTitle(e), memory.EnforcementIssueBody(e), o.enforceIssueState())
	if err != nil {
		return memory.Entry{}, nil, fmt.Errorf("orchestrator: create the enforcement issue: %w", err)
	}
	identifier, url := "", ""
	if created != nil {
		identifier = created.Identifier
		if created.URL != nil {
			url = *created.URL
		}
	}
	out, err := store.DecideEnforcement(id, true, identifier, url)
	if err != nil {
		return memory.Entry{}, created, err
	}
	slog.Info("orchestrator: lesson enforcement accepted; issue created",
		"memory_id", id, "issue_identifier", identifier, "kind", e.Lesson.Enforcement.Kind)
	return out, created, nil
}

// DismissLessonEnforcement turns down the lesson's pending enforcement
// suggestion; it is not suggested again for this lesson.
func (o *Orchestrator) DismissLessonEnforcement(id string) (memory.Entry, error) {
	out, err := memory.Store{WorkflowPath: o.cfg.WorkflowPath}.DecideEnforcement(id, false, "", "")
	if err == nil {
		slog.Info("orchestrator: lesson enforcement dismissed", "memory_id", id)
	}
	return out, err
}
