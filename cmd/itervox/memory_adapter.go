package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/memory"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// memoryAdapter implements server.MemoryClient for the Memory page (#202).
// Memory lives in files the daemon reads again for every run, so the page
// edits those files directly through memory.Store (no cfg field, no
// reload). cfg.WorkflowPath and cfg.Memory are read-only after startup; run
// history comes from the orchestrator's history accessor (RunHistory).
type memoryAdapter struct {
	cfg     *config.Config
	history func() []orchestrator.CompletedRun
}

// memoryRunRefsPerEntry bounds the runs listed per entry.
const memoryRunRefsPerEntry = 20

// memoryPromoteDiffTimeout bounds the local git diff a promote returns.
const memoryPromoteDiffTimeout = 10 * time.Second

func (a *memoryAdapter) store() memory.Store {
	return memory.Store{WorkflowPath: a.cfg.WorkflowPath}
}

// runRefs maps each memory id (lowercase) to the newest runs that used it.
func (a *memoryAdapter) runRefs() map[string][]server.MemoryRunRef {
	out := map[string][]server.MemoryRunRef{}
	if a.history == nil {
		return out
	}
	hist := a.history()
	for i := len(hist) - 1; i >= 0; i-- {
		r := hist[i]
		for _, id := range r.MemoryIDs {
			key := strings.ToLower(id)
			if len(out[key]) >= memoryRunRefsPerEntry {
				continue
			}
			out[key] = append(out[key], server.MemoryRunRef{
				RunID: r.RunID, Identifier: r.Identifier, Status: r.Status, FinishedAt: r.FinishedAt,
			})
		}
	}
	return out
}

func (a *memoryAdapter) view(e memory.Entry, refs map[string][]server.MemoryRunRef) server.MemoryEntryView {
	runs := refs[strings.ToLower(e.ID)]
	if runs == nil {
		runs = []server.MemoryRunRef{}
	}
	return server.MemoryEntryView{Entry: e, Path: memory.PathFor(a.cfg.WorkflowPath, e.Scope), Runs: runs}
}

// ListMemory implements server.MemoryClient.
func (a *memoryAdapter) ListMemory() server.MemoryView {
	refs := a.runRefs()
	out := server.MemoryView{
		Enabled:           a.cfg.Memory.Enabled,
		TokenBudget:       a.cfg.Memory.TokenBudget,
		PromptShareTokens: a.cfg.Memory.PromptShareTokens,
		SchemaURL:         memory.SchemaURL,
		Files:             []server.MemoryFileView{},
		Entries:           []server.MemoryEntryView{},
	}
	for _, f := range a.store().List() {
		fv := server.MemoryFileView{Scope: f.Scope, Path: f.Path, Exists: f.Exists, Writable: f.Path != "" && f.Err == nil}
		if f.Err != nil {
			fv.Error = f.Err.Error()
		}
		out.Files = append(out.Files, fv)
		for _, e := range f.Entries {
			out.Entries = append(out.Entries, a.view(e, refs))
		}
	}
	return out
}

// CreateMemory implements server.MemoryClient.
func (a *memoryAdapter) CreateMemory(e memory.Entry) (server.MemoryEntryView, error) {
	if e.Source.IsZero() {
		e.Source.Kind = "manual"
	}
	out, err := a.store().Create(e)
	if err != nil {
		return server.MemoryEntryView{}, err
	}
	slog.Info("memory: entry added from the dashboard", "id", out.ID, "scope", out.Scope, "type", out.Type)
	return a.view(out, a.runRefs()), nil
}

// UpdateMemory implements server.MemoryClient.
func (a *memoryAdapter) UpdateMemory(id string, p memory.Patch) (server.MemoryEntryView, error) {
	out, err := a.store().Update(id, p)
	if err != nil {
		return server.MemoryEntryView{}, err
	}
	slog.Info("memory: entry updated from the dashboard", "id", out.ID, "scope", out.Scope)
	return a.view(out, a.runRefs()), nil
}

// DeleteMemory implements server.MemoryClient.
func (a *memoryAdapter) DeleteMemory(id string) (server.MemoryEntryView, error) {
	out, err := a.store().Delete(id)
	if err != nil {
		return server.MemoryEntryView{}, err
	}
	slog.Info("memory: entry deleted from the dashboard", "id", out.ID, "scope", out.Scope)
	return a.view(out, a.runRefs()), nil
}

// PinMemory implements server.MemoryClient.
func (a *memoryAdapter) PinMemory(id string, pinned bool) (server.MemoryEntryView, error) {
	out, err := a.store().Pin(id, pinned)
	if err != nil {
		return server.MemoryEntryView{}, err
	}
	return a.view(out, a.runRefs()), nil
}

// PromoteMemory implements server.MemoryClient: the entry moves into
// .itervox/memory.json and the result carries that file's git diff, so the
// operator sees exactly what the commit will add.
func (a *memoryAdapter) PromoteMemory(ctx context.Context, id string) (server.MemoryPromoteResult, error) {
	out, err := a.store().Promote(id)
	if err != nil {
		return server.MemoryPromoteResult{}, err
	}
	slog.Info("memory: entry promoted to the project memory", "id", out.ID)
	path := memory.ProjectPath(a.cfg.WorkflowPath)
	res := server.MemoryPromoteResult{Entry: a.view(out, a.runRefs()), Path: path}
	dctx, cancel := context.WithTimeout(ctx, memoryPromoteDiffTimeout)
	defer cancel()
	res.Diff, res.DiffNote = memoryFileDiff(dctx, path)
	return res, nil
}

// memoryFileDiff is `git diff` of path in its repository: the working-tree
// change of a tracked file, or the whole file as new when git does not track
// it yet. Outside a repository (or when git fails) the diff is empty and the
// note says why. Read-only: it never touches the index.
func memoryFileDiff(ctx context.Context, path string) (diff, note string) {
	dir := filepath.Dir(path)
	top, err := gitexec.Command(ctx, dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", "not a git repository: no diff to show"
	}
	root := strings.TrimSpace(string(top))
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	if gitexec.Command(ctx, root, "ls-files", "--error-unmatch", "--", rel).Run() == nil {
		b, err := gitexec.Command(ctx, root, "diff", "--no-color", "--", rel).Output()
		if err != nil {
			return "", fmt.Sprintf("git diff failed: %v", err)
		}
		return string(b), ""
	}
	// Untracked: show the whole file as added. `git diff --no-index` exits
	// 1 when the files differ, which is the expected outcome here.
	b, err := gitexec.Command(ctx, root, "diff", "--no-color", "--no-index", "--", "/dev/null", rel).Output()
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1) {
		return "", fmt.Sprintf("git diff failed: %v", err)
	}
	return string(b), "the file is not committed yet: the diff shows it as new"
}

// ensure the adapter keeps the interface's shape.
var _ server.MemoryClient = (*memoryAdapter)(nil)
