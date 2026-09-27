package logbuffer

import (
	"log/slog"
	"os"
)

// countDiskLines returns identifier's current-file line count with exactly
// tailLines' semantics, streaming the file instead of materialising it.
// Returns 0 for a missing or empty file.
func countDiskLines(dir, identifier string) int {
	n, _ := countFile(issuePath(dir, identifier))
	return int(n)
}

// appendToDisk writes log lines, in order, to the per-identifier file in dir,
// with no size cap or rotation. A failure is logged and returned; the lines
// stay in memory. Production appends go through the writer's appendRotating
// (CORE-036); this plain append remains for test seams that stand in for it.
func appendToDisk(dir, identifier string, lines []string) error {
	p := issuePath(dir, identifier)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("logbuffer: failed to create log dir", "dir", dir, "error", err)
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("logbuffer: failed to open log file", "path", p, "error", err)
		return err
	}
	defer func() { _ = f.Close() }()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			slog.Warn("logbuffer: failed to write log line", "path", p, "error", err)
			return err
		}
	}
	return nil
}
