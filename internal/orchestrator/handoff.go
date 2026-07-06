package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// HandoffDirRelPath is the path (relative to the workspace root) where
// per-issue agent handoff files are stored. The directory is created lazily
// by the first agent that writes into it; it is committable via a .gitignore
// carve-out patched by `itervox init` and `itervox init --update`.
const HandoffDirRelPath = ".itervox/handoff"

// DefaultHandoffBudgetBytes caps the prerendered prior-handoff block injected
// into every worker's prompt. Approximately 30 KB ≈ 7-8K tokens, leaving room
// for the rest of the prompt context. Files dropped to fit the budget are
// always the oldest, with a marker indicating truncation.
const DefaultHandoffBudgetBytes = 30 * 1024

// handoffRunTimestamp returns an ISO8601 timestamp safe for filenames
// (colons replaced with hyphens) at the given instant. The lexicographic
// sort order of these timestamps is identical to chronological order, so
// `filepath.Glob` results are already in dispatch order.
//
// Includes millisecond precision so rapid retries within the same wall-clock
// second produce distinct filenames. Without this, attempt N+1 dispatched
// in the same second as attempt N would write to the same path, and the
// terminal-state `.partial.md` rename would overwrite the prior attempt's
// partial deliverable.
func handoffRunTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15-04-05.000Z")
}

// handoffPathFor builds the canonical handoff file path (relative to the
// workspace root) for a given run timestamp and profile name. Profile name
// is slugified minimally — spaces become hyphens — so filenames are
// glob-friendly.
func handoffPathFor(runTimestamp, profileName string) string {
	role := strings.ReplaceAll(strings.TrimSpace(profileName), " ", "-")
	if role == "" {
		role = "agent"
	}
	return filepath.Join(HandoffDirRelPath, fmt.Sprintf("%s_%s.md", runTimestamp, role))
}

// buildRunContextBlock renders the per-run metadata block appended to every
// worker's prompt. The agent uses these values to decide where to write its
// own handoff deliverable.
func buildRunContextBlock(runTimestamp, handoffPath string) string {
	return strings.Join([]string{
		"## Run Context",
		"",
		fmt.Sprintf("- run.timestamp: `%s`", runTimestamp),
		fmt.Sprintf("- run.handoff_path: `%s`", handoffPath),
		"",
		"Write your deliverable Markdown to `run.handoff_path` before exiting.",
		"It will be visible to every subsequent agent on this branch as a prior handoff.",
	}, "\n")
}

// buildHandoffContextBlock reads every `.md` file in
// `<workspacePath>/.itervox/handoff/`, sorts them by filename (lexicographic
// equals chronological because the prefix is ISO8601), applies a byte budget
// (dropping oldest files first with a truncation marker), and returns a
// `## Prior Agent Handoffs` Markdown block suitable for appending to the
// agent's prompt. Returns "" when no handoff files exist.
//
// Files with the `.partial.md` suffix (recorded when a prior worker exited
// non-success mid-deliverable) are included and explicitly marked so the
// agent does not mistake them for completed work.
func buildHandoffContextBlock(workspacePath string, budget int) string {
	if workspacePath == "" {
		return ""
	}
	if budget <= 0 {
		budget = DefaultHandoffBudgetBytes
	}
	dir := filepath.Join(workspacePath, HandoffDirRelPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No handoff dir yet — first agent in the pipeline. Not an error.
		return ""
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names) // ISO8601 prefix → lexicographic = chronological

	type rendered struct {
		name string
		body string
	}
	pieces := make([]rendered, 0, len(names))
	totalBytes := 0
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			slog.Warn("handoff: skipping unreadable file",
				"path", filepath.Join(dir, name), "error", err)
			continue
		}
		body := strings.TrimSpace(string(data))
		marker := ""
		if strings.HasSuffix(name, ".partial.md") {
			marker = " (partial — prior agent exited before completing this deliverable)"
		}
		piece := fmt.Sprintf("### %s%s\n\n%s", name, marker, body)
		pieces = append(pieces, rendered{name: name, body: piece})
		totalBytes += len(piece)
	}
	if len(pieces) == 0 {
		return ""
	}

	// Budget enforcement: drop oldest first until under budget.
	truncated := 0
	for totalBytes > budget && len(pieces) > 1 {
		dropped := pieces[0]
		totalBytes -= len(dropped.body)
		pieces = pieces[1:]
		truncated++
	}

	var b strings.Builder
	b.WriteString("## Prior Agent Handoffs\n\n")
	b.WriteString("Listed in chronological order (oldest first). Each file is one prior agent's deliverable on this issue's branch.\n")
	if truncated > 0 {
		fmt.Fprintf(&b, "\n_[%d earlier handoffs truncated to fit budget — see `.itervox/handoff/` for full history]_\n", truncated)
	}
	for _, p := range pieces {
		b.WriteString("\n")
		b.WriteString(p.body)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// synthesizedHandoffHeader marks a handoff the orchestrator wrote on the
// agent's behalf. Subsequent agents (and humans) can distinguish it from a
// deliverable the agent authored itself.
const synthesizedHandoffHeader = "> **Synthesized handoff** — the agent exited successfully without writing " +
	"its handoff deliverable. The orchestrator captured the session summary instead " +
	"(spec F2: updating shared state is part of the definition of done)."

// ensureHandoffOnSuccess enforces F2 ("update the shared state MUST be part
// of the definition of done") on the worker success path: if the run's
// handoff file is missing or empty, the orchestrator synthesizes one from
// the session summary, marked as synthesized. Returns synthesized=true when
// a file was written. A non-empty agent-authored handoff is never touched.
func ensureHandoffOnSuccess(workspacePath, handoffRelPath, sessionSummary string) (synthesized bool, err error) {
	if workspacePath == "" || handoffRelPath == "" {
		return false, nil
	}
	path := filepath.Join(workspacePath, handoffRelPath)
	if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() && info.Size() > 0 {
		return false, nil // agent wrote its own handoff — done is done
	}
	body := strings.TrimSpace(sessionSummary)
	if body == "" {
		body = "_The agent produced no session summary for this run._"
	}
	content := synthesizedHandoffHeader + "\n\n" + body + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("handoff: mkdir for synthesized handoff: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, fmt.Errorf("handoff: write synthesized handoff: %w", err)
	}
	return true, nil
}

// markHandoffPartial renames the worker's in-flight handoff file to
// `<basename>.partial.md` so subsequent agents see it as an incomplete
// deliverable. Called by the worker when it exits with a non-success
// reason that is not `TerminalInputRequired` (input-required is a
// pause/resume, not a failure — its handoff stays as-is).
//
// No-op when the file does not exist (agent never wrote it) or is
// already marked partial.
func markHandoffPartial(workspacePath, handoffRelPath string) error {
	if workspacePath == "" || handoffRelPath == "" {
		return nil
	}
	src := filepath.Join(workspacePath, handoffRelPath)
	if strings.HasSuffix(src, ".partial.md") {
		return nil
	}
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("handoff: stat %s: %w", src, err)
	}
	if info.IsDir() {
		return nil
	}
	dst := strings.TrimSuffix(src, ".md") + ".partial.md"
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("handoff: rename to partial %s: %w", src, err)
	}
	return nil
}

// markLatestHandoffPartial finds the most recent (by mtime) handoff file
// authored by the named profile in `<workspacePath>/.itervox/handoff/`
// and renames it to `<basename>.partial.md`. Used by the orchestrator
// when a worker exits with a non-success terminal reason (TerminalFailed,
// TerminalStalled) — including the stall case where the orchestrator
// synthesizes the exit event itself rather than the worker.
//
// `notBefore` gates which files are eligible: only files with `mtime >=
// notBefore` are considered. The orchestrator passes the worker's
// `StartedAt` here so a failed worker that did NOT write its own handoff
// cannot accidentally rename a predecessor's complete handoff to
// `.partial.md`. Passing the zero time disables the gate (legacy behavior;
// used in tests that don't model worker lifetime).
//
// No-op when: no matching files exist (agent never wrote one), no matching
// files are newer than notBefore, or the workspace path is empty.
// The function is filesystem-driven so it does not require the
// orchestrator to remember each worker's exact run timestamp.
func markLatestHandoffPartial(workspacePath, profileName string, notBefore time.Time) error {
	if workspacePath == "" {
		return nil
	}
	role := strings.ReplaceAll(strings.TrimSpace(profileName), " ", "-")
	if role == "" {
		role = "agent"
	}
	dir := filepath.Join(workspacePath, HandoffDirRelPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("handoff: read dir %s: %w", dir, err)
	}

	suffix := "_" + role + ".md"
	var (
		latestName string
		latestMod  time.Time
	)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		mod := info.ModTime()
		// Gate: ignore files older than the current worker started. They
		// belong to predecessor runs whose handoffs should remain intact.
		if !notBefore.IsZero() && mod.Before(notBefore) {
			continue
		}
		if mod.After(latestMod) {
			latestName = name
			latestMod = mod
		}
	}
	if latestName == "" {
		return nil
	}
	return markHandoffPartial(workspacePath, filepath.Join(HandoffDirRelPath, latestName))
}
