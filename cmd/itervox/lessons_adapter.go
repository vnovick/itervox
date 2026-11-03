package main

import (
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/memory"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// Lessons (#203) on the Memory page: approve / reject on the memory files
// (memory.Store, same lock as every other memory write), the lessons
// configuration and the "not helping" flags for GET /api/v1/memory, and the
// proposed-lesson count the snapshot carries for the nav item.

// newMemoryAdapter is the Memory page's client wired to the orchestrator:
// run history for usage and lesson health, and the reviewer profile as the
// default lesson drafter.
func newMemoryAdapter(cfg *config.Config, orch *orchestrator.Orchestrator) *memoryAdapter {
	return &memoryAdapter{cfg: cfg, history: orch.RunHistory, reviewerProfile: func() string {
		p, _ := orch.ReviewerCfg()
		return p
	}}
}

// ApproveLesson implements server.MemoryClient.
func (a *memoryAdapter) ApproveLesson(id string, opt memory.ApproveOptions) (server.MemoryEntryView, error) {
	out, err := a.store().ApproveLesson(id, opt)
	if err != nil {
		return server.MemoryEntryView{}, err
	}
	slog.Info("memory: lesson approved from the dashboard", "id", out.ID, "scope", out.Scope, "edited", opt.Text != nil)
	return a.view(out, a.runRefs()), nil
}

// RejectLesson implements server.MemoryClient.
func (a *memoryAdapter) RejectLesson(id string) (server.MemoryEntryView, error) {
	out, err := a.store().RejectLesson(id)
	if err != nil {
		return server.MemoryEntryView{}, err
	}
	slog.Info("memory: lesson rejected from the dashboard", "id", out.ID, "scope", out.Scope)
	return a.view(out, a.runRefs()), nil
}

// lessonsView is memory.lessons for the Lessons tab.
func (a *memoryAdapter) lessonsView() server.MemoryLessonsView {
	lc := a.cfg.Memory.Lessons
	drafter := "template"
	name := lc.Profile
	if name == "" && a.reviewerProfile != nil {
		name = a.reviewerProfile()
	}
	if name != "" && name != config.LessonProfileTemplate {
		drafter = "profile:" + name
	}
	rejected, _ := a.store().Rejected()
	return server.MemoryLessonsView{
		Enabled: a.cfg.Memory.Enabled && lc.Enabled, Drafter: drafter, Scope: memory.Scope(lc.Scope),
		AutoApprovePrivate: lc.AutoApprovePrivate, AutoApproveProject: lc.AutoApproveProject,
		MaxPerIssue: lc.MaxPerIssue, RateWindowHours: lc.RateWindowHours, StaleAfterRuns: lc.StaleAfterRuns,
		Rejected: len(rejected),
	}
}

// annotateLessons fills the lessons view and flags approved lessons that
// are not helping, judged against run history.
func (a *memoryAdapter) annotateLessons(v *server.MemoryView) {
	v.Lessons = a.lessonsView()
	if a.history == nil {
		return
	}
	hist := a.history()
	runs := make([]memory.RunUse, 0, len(hist))
	for _, r := range hist {
		runs = append(runs, memory.RunUse{
			MemoryIDs: r.MemoryIDs, FinishedAt: r.FinishedAt, Failed: r.Status == "failed" || r.Status == "stalled",
		})
	}
	for i := range v.Entries {
		v.Entries[i].NotHelping, v.Entries[i].NotHelpingReason = memory.HealthOf(v.Entries[i].Entry, runs, a.cfg.Memory.Lessons.StaleAfterRuns)
	}
}

// proposedLessonCounter counts the proposed entries of the memory files for
// the snapshot, re-reading a file only when its size or modification time
// changed (the snapshot is rebuilt on every state change).
type proposedLessonCounter struct {
	workflowPath string
	mu           sync.Mutex
	seen         map[string]counterStamp
}

type counterStamp struct {
	size  int64
	mod   time.Time
	count int
}

func newProposedLessonCounter(workflowPath string) *proposedLessonCounter {
	return &proposedLessonCounter{workflowPath: workflowPath, seen: map[string]counterStamp{}}
}

// Count is the number of proposed entries across both memory files.
func (c *proposedLessonCounter) Count() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, sc := range memory.Scopes {
		path := memory.PathFor(c.workflowPath, sc)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			delete(c.seen, path)
			continue
		}
		if st, ok := c.seen[path]; ok && st.size == info.Size() && st.mod.Equal(info.ModTime()) {
			total += st.count
			continue
		}
		n := 0
		for _, e := range memory.ReadFile(sc, path).Entries {
			if e.Status == memory.StatusProposed {
				n++
			}
		}
		c.seen[path] = counterStamp{size: info.Size(), mod: info.ModTime(), count: n}
		total += n
	}
	return total
}
