package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// writeStartupErrorMarker writes a STARTUP_ERROR.md file inside the
// .itervox/ directory next to the WORKFLOW.md before the daemon exits, so an
// operator running the daemon under nohup / launchd has a durable
// human-readable record of what broke. The file is one-way: cleared on the
// next healthy startup so its mere presence is the "last startup failed"
// signal. P0-D.
func writeStartupErrorMarker(workflowPath string, loadErr error) {
	if loadErr == nil {
		return
	}
	dir := filepath.Dir(workflowPath)
	if dir == "" {
		dir = "."
	}
	itervoxDir := filepath.Join(dir, ".itervox")
	if err := os.MkdirAll(itervoxDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "startup: cannot prepare %s: %v\n", itervoxDir, err)
		return
	}
	path := filepath.Join(itervoxDir, "STARTUP_ERROR.md")
	body := renderStartupErrorMarker(workflowPath, loadErr, time.Now().UTC())
	if err := atomicfs.WriteFile(path, []byte(body), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "startup: cannot write %s: %v\n", path, err)
	}
}

func renderStartupErrorMarker(workflowPath string, loadErr error, now time.Time) string {
	var b strings.Builder
	b.WriteString("# itervox startup error\n\n")
	fmt.Fprintf(&b, "- timestamp: %s\n", now.Format(time.RFC3339))
	fmt.Fprintf(&b, "- workflow: %s\n", workflowPath)
	b.WriteString("\n## Error\n\n```\n")
	b.WriteString(loadErr.Error())
	b.WriteString("\n```\n\n")
	b.WriteString("## Suggested fix\n\n")
	b.WriteString(suggestStartupErrorFix(loadErr))
	b.WriteString("\n")
	return b.String()
}

func suggestStartupErrorFix(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "yaml:") || strings.Contains(msg, "unmarshal"):
		return "WORKFLOW.md YAML is malformed. Open the file and check the line/column from the error string above; the most common cause is mixed indentation under `agent:`."
	case strings.Contains(msg, "itervox_schema_version"):
		return "Run `itervox init --update --workflow WORKFLOW.md` to upgrade the workflow to schema 2."
	case strings.Contains(msg, "missing tracker.api_key"):
		return "Tracker API key is missing. Set `tracker.api_key: $LINEAR_API_KEY` (or equivalent) and ensure the env var resolves at startup."
	default:
		return "See the error above. Fix WORKFLOW.md and restart the daemon."
	}
}

// startupErrorSummary returns a one-line pointer to the STARTUP_ERROR.md
// marker when one exists next to the workflow, or "" when the last startup
// was healthy. Consumed by the heartbeat writer so HEARTBEAT.md reports
// `Daemon: degraded` plus a `Last startup error:` line while the marker is
// present (todolist6 P0-D / gaps_11 G-15).
func startupErrorSummary(workflowPath string) string {
	dir := filepath.Dir(workflowPath)
	if dir == "" {
		dir = "."
	}
	path := filepath.Join(dir, ".itervox", "STARTUP_ERROR.md")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return fmt.Sprintf("see %s (clear with `itervox doctor --clear-startup-error`)", path)
}

// clearStartupErrorMarker removes a previously written STARTUP_ERROR.md so
// the next healthy boot leaves a clean .itervox/. Called after the first
// successful snapshot is produced.
func clearStartupErrorMarker(workflowPath string) {
	dir := filepath.Dir(workflowPath)
	if dir == "" {
		dir = "."
	}
	path := filepath.Join(dir, ".itervox", "STARTUP_ERROR.md")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "startup: cannot clear %s: %v\n", path, err)
	}
}
