package orchestrator

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/memory"
)

// Lesson drafting (#203), off the event loop. A short call to the lesson
// profile's backend (memory.lessons.profile, else the reviewer profile)
// through the same agent.Runner every worker uses, with a hard budget: the
// turn is cancelled once its reported tokens pass
// memory.lessons.draft_token_budget or memory.lessons.draft_timeout_ms
// elapses. No profile, a failed call, an over-budget call or unusable
// output all fall back to memory.TemplateDraft, which calls nothing.

// lessonDraftPlan is what a drafting goroutine needs from the config,
// resolved on the event loop (cfgMu fields are read under the lock here,
// never off the loop).
type lessonDraftPlan struct {
	// Profile is the drafting profile's name; "" drafts from the template.
	Profile string
	// Command is the runner command for the profile's backend.
	Command     string
	TokenBudget int
	TimeoutMs   int
}

// lessonDraftPlan resolves the drafting profile: memory.lessons.profile,
// else agent.reviewer_profile; "template", an unknown profile or none
// drafts from the template.
func (o *Orchestrator) lessonDraftPlan() lessonDraftPlan {
	lc := o.cfg.Memory.Lessons
	plan := lessonDraftPlan{TokenBudget: lc.DraftTokenBudget, TimeoutMs: lc.DraftTimeoutMs}
	if lc.Profile == config.LessonProfileTemplate || o.runner == nil {
		return plan
	}
	o.cfgMu.RLock()
	name := lc.Profile
	if name == "" {
		name = o.cfg.Agent.ReviewerProfile
	}
	profile, ok := o.cfg.Agent.Profiles[name]
	o.cfgMu.RUnlock()
	if name == "" || !ok {
		if name != "" {
			slog.Warn("orchestrator: lesson profile not found; drafting lessons from the template", "profile", name)
		}
		return plan
	}
	target := resolveDispatchTarget(dispatchTargetInput{
		DefaultCommand: o.cfg.Agent.Command, DefaultBackend: o.cfg.Agent.Backend, Profile: &profile,
	})
	if target.RunnerCommand == "" {
		return plan
	}
	plan.Profile, plan.Command = name, target.RunnerCommand
	return plan
}

// draftAndPropose drafts a candidate and writes the proposal. A correction
// rejected before stops here; one that matches an existing lesson skips
// the drafting call (the proposal only bumps that lesson's evidence).
// Off the event loop: touches files, never State.
func (o *Orchestrator) draftAndPropose(ctx context.Context, store memory.Store, cand memory.Candidate, plan lessonDraftPlan, opts memory.ProposeOptions) LessonReport {
	pre := store.PrecheckLesson(cand.CorrectionText())
	if pre == memory.LessonSuppressed {
		return LessonReport{Outcome: memory.LessonSuppressed}
	}
	draft := memory.TemplateDraft(cand)
	if pre == memory.LessonNew && plan.Command != "" {
		d, err := o.draftWithAgent(ctx, cand, plan)
		if err != nil {
			slog.Warn("orchestrator: lesson drafting call failed; using the template",
				"issue_identifier", cand.Issue, "profile", plan.Profile, "error", err)
		} else {
			draft = d
		}
	}
	res, err := store.ProposeLesson(memory.NewLessonEntry(cand, draft), opts)
	if err != nil {
		return LessonReport{Err: err, DraftedBy: draft.DraftedBy}
	}
	return LessonReport{Outcome: res.Outcome, EntryID: res.Entry.ID, DraftedBy: draft.DraftedBy}
}

// errLessonOverBudget: the drafting call passed its token budget.
var errLessonOverBudget = errors.New("orchestrator: lesson drafting call passed its token budget")

// draftWithAgent runs the budgeted drafting turn. It runs in an empty
// temporary directory with the sandbox permission mode: drafting needs no
// tools and must not touch a workspace.
func (o *Orchestrator) draftWithAgent(ctx context.Context, cand memory.Candidate, plan lessonDraftPlan) (memory.Draft, error) {
	dctx, cancel := context.WithTimeout(ctx, time.Duration(plan.TimeoutMs)*time.Millisecond)
	defer cancel()
	var over atomic.Bool
	onProgress := func(r agent.TurnResult) {
		if r.InputTokens+r.OutputTokens > plan.TokenBudget {
			over.Store(true)
			cancel()
		}
	}
	dir, err := os.MkdirTemp("", "itervox-lesson-")
	if err != nil {
		return memory.Draft{}, fmt.Errorf("orchestrator: lesson draft dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	var sessionID string
	res, err := o.runner.RunTurn(dctx, o.logger().With("issue_identifier", cand.Issue, "lesson_draft", true),
		onProgress, &sessionID, lessonDraftPrompt(cand), dir, plan.Command, "", "",
		plan.TimeoutMs, plan.TimeoutMs, agent.PermissionSandbox)
	if over.Load() || res.InputTokens+res.OutputTokens > plan.TokenBudget {
		return memory.Draft{}, errLessonOverBudget
	}
	if err != nil {
		return memory.Draft{}, err
	}
	if res.Failed {
		return memory.Draft{}, fmt.Errorf("orchestrator: lesson drafting turn failed: %s", boundUTF8(res.FailureText, 200))
	}
	d, ok := parseLessonDraft(cmp.Or(strings.TrimSpace(res.ResultText), strings.TrimSpace(res.LastText)))
	if !ok {
		return memory.Draft{}, errors.New("orchestrator: lesson drafting turn returned no usable lesson")
	}
	d.DraftedBy = "profile:" + plan.Profile
	return d, nil
}

// lessonEvidenceBytes bounds each piece of evidence in the drafting prompt,
// keeping the call short whatever the correction's size.
const lessonEvidenceBytes = 600

// lessonDraftPrompt is the drafting turn's prompt: the evidence and the
// output contract.
func lessonDraftPrompt(c memory.Candidate) string {
	var b strings.Builder
	b.WriteString("You are drafting ONE short \"lesson learned\" for the memory of an AI coding agent, from the evidence below.\n")
	b.WriteString("Do not run tools, read files or change anything. Reply with ONLY a JSON object, no prose and no code fence:\n")
	b.WriteString(`{"text": "...", "situation": "...", "wentWrong": "...", "worked": "..."}` + "\n")
	b.WriteString("text: one or two sentences shaped \"When <situation>, do <what worked> instead of <what went wrong>.\", at most 300 characters, general enough to help on future issues (no issue numbers, people or secrets).\n")
	b.WriteString("situation / wentWrong / worked: a short phrase each.\n\n")
	fmt.Fprintf(&b, "Trigger: %s\n", c.Trigger)
	if c.Title != "" {
		fmt.Fprintf(&b, "Issue title: %s\n", boundUTF8(c.Title, 200))
	}
	if c.Situation != "" {
		fmt.Fprintf(&b, "Situation: %s\n", boundUTF8(c.Situation, lessonEvidenceBytes))
	}
	if c.WentWrong != "" {
		fmt.Fprintf(&b, "What went wrong: %s\n", boundUTF8(c.WentWrong, lessonEvidenceBytes))
	}
	b.WriteString("Human correction / what changed before it worked:\n")
	for _, s := range c.Corrections {
		fmt.Fprintf(&b, "- %s\n", boundUTF8(strings.Join(strings.Fields(s), " "), lessonEvidenceBytes))
	}
	return b.String()
}

// parseLessonDraft reads the drafting turn's output: the JSON object when
// there is one, else the whole output as the lesson text.
func parseLessonDraft(out string) (memory.Draft, bool) {
	if out == "" {
		return memory.Draft{}, false
	}
	var d struct {
		Text      string `json:"text"`
		Situation string `json:"situation"`
		WentWrong string `json:"wentWrong"`
		Worked    string `json:"worked"`
	}
	if i, j := strings.Index(out, "{"), strings.LastIndex(out, "}"); i >= 0 && j > i && json.Unmarshal([]byte(out[i:j+1]), &d) == nil {
		if t := strings.TrimSpace(d.Text); t != "" {
			return memory.Draft{
				Text: boundUTF8(t, memory.MaxTextBytes), Situation: boundUTF8(d.Situation, lessonEvidenceBytes),
				WentWrong: boundUTF8(d.WentWrong, lessonEvidenceBytes), Worked: boundUTF8(d.Worked, lessonEvidenceBytes),
			}, true
		}
		return memory.Draft{}, false
	}
	if strings.HasPrefix(out, "{") {
		return memory.Draft{}, false
	}
	return memory.Draft{Text: boundUTF8(strings.Join(strings.Fields(out), " "), memory.MaxTextBytes)}, true
}
