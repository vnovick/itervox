package main

import (
	"context"
	"errors"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/workspace"
)

// changesAdapter backs the run-context and diff endpoints (#215) with the
// orchestrator. RunContext reads the published snapshot; Diff, FileDiff and
// Handoffs run git / read files in the HTTP handler's goroutine, never on
// the event loop.
type changesAdapter struct {
	orch *orchestrator.Orchestrator
}

func (c changesAdapter) RunContext(identifier string) (server.RunContextRow, bool) {
	v, ok := c.orch.RunContext(identifier)
	if !ok {
		return server.RunContextRow{}, false
	}
	return runContextRow(v), true
}

func runContextRow(v orchestrator.RunContextView) server.RunContextRow {
	row := server.RunContextRow{
		RunID: v.RunID, Live: v.Live, Kind: v.Kind, Profile: v.ProfileName, Backend: v.Backend,
		StartedAt: v.StartedAt, FinishedAt: v.FinishedAt, Outcome: v.Outcome, UpdatedAt: v.UpdatedAt,
		BaseBranch: v.BaseBranch, CurrentStep: v.CurrentStep, ToolCalls: v.ToolCalls,
		TurnCount: v.TurnCount, InputTokens: v.InputTokens, OutputTokens: v.OutputTokens, TotalTokens: v.TotalTokens,
		PendingInput: v.PendingInput, PendingApproval: v.PendingApproval, QueuedSteers: v.QueuedSteers,
		Steers:          make([]server.RunSteerRow, 0, len(v.Steers)),
		GuardrailBlocks: v.GuardrailBlocks, LastGuardrailKind: v.LastGuardrailKind,
		Playbook: v.Playbook, // #218
	}
	if v.LastToolName != "" {
		row.LastToolCall = &server.RunToolCall{Name: v.LastToolName, Target: v.LastToolTarget, At: v.LastToolAt}
	}
	for _, s := range v.Steers {
		row.Steers = append(row.Steers, server.RunSteerRow{Text: s.Text, At: s.At, Delivery: s.Delivery})
	}
	return row
}

func (c changesAdapter) Diff(ctx context.Context, identifier, base string) (server.DiffResponse, error) {
	sum, err := c.orch.WorktreeDiff(ctx, identifier, base)
	if err != nil {
		return server.DiffResponse{}, mapChangesErr(err)
	}
	resp := server.DiffResponse{Base: sum.Base, MergeBase: sum.MergeBase, Truncated: sum.Truncated,
		Files: make([]server.DiffFileRow, 0, len(sum.Files))}
	for _, f := range sum.Files {
		resp.Files = append(resp.Files, diffFileRow(f))
	}
	return resp, nil
}

func (c changesAdapter) FileDiff(ctx context.Context, identifier, base, file string) (server.FileDiffResponse, error) {
	fd, err := c.orch.WorktreeFileDiff(ctx, identifier, base, file)
	if err != nil {
		return server.FileDiffResponse{}, mapChangesErr(err)
	}
	return server.FileDiffResponse{File: diffFileRow(fd.DiffFile), Patch: fd.Patch, Truncated: fd.Truncated,
		Bytes: fd.Bytes, MaxBytes: orchestrator.FileDiffMaxBytes}, nil
}

func (c changesAdapter) Handoffs(identifier string) []server.HandoffRow {
	refs := c.orch.Handoffs(identifier)
	rows := make([]server.HandoffRow, 0, len(refs))
	for _, h := range refs {
		rows = append(rows, server.HandoffRow{Title: h.Title, Path: h.Path, Partial: h.Partial})
	}
	return rows
}

func diffFileRow(f workspace.DiffFile) server.DiffFileRow {
	return server.DiffFileRow{Path: f.Path, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, Binary: f.Binary}
}

// mapChangesErr maps orchestrator / workspace diff errors onto the
// server's sentinels; anything else passes through (500, or 504 on a
// deadline).
func mapChangesErr(err error) error {
	switch {
	case errors.Is(err, orchestrator.ErrNoWorktree):
		return server.ErrNoWorktree
	case errors.Is(err, orchestrator.ErrBadDiffBase):
		return server.ErrBadDiffBase
	case errors.Is(err, workspace.ErrDiffBaseNotFound):
		return server.ErrDiffBaseNotFound
	case errors.Is(err, workspace.ErrDiffFileNotChanged):
		return server.ErrDiffFileNotChanged
	}
	return err
}
