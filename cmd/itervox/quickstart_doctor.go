package main

import (
	"fmt"
	"io"
	"strings"
)

// Quickstart's doctor step: the same checks as `itervox doctor --fix`, run
// in-process, but only the failures are printed, each with its fix. A run
// that passes prints one line (and how many warnings `itervox doctor` would
// show); the full report stays one command away.

// quickstartDoctor runs the checks and returns doctor's exit code.
func quickstartDoctor(workflowPath string, yes bool, in io.Reader, out io.Writer) int {
	report := doctorFixReport(workflowPath, yes, in, out)
	rendered := renderDoctorReport(report)
	code := doctorExitCode(report)
	if code != 0 {
		_, _ = fmt.Fprintf(out, "\nitervox quickstart: doctor found problems:\n")
		failures := doctorFailureLines(report, rendered)
		if len(failures) == 0 {
			// A failure the filter does not recognise: show everything
			// rather than nothing.
			_, _ = fmt.Fprint(out, rendered)
		}
		for _, line := range failures {
			_, _ = fmt.Fprintln(out, "  "+line)
		}
		_, _ = fmt.Fprintf(out, "\nitervox quickstart: fix them and run `itervox quickstart` again (`itervox doctor%s` shows the full report).\n",
			quickstartWorkflowFlag(workflowPath))
		return code
	}
	warnings := 0
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "WARNING:") || strings.HasPrefix(strings.TrimSpace(line), "[warn]") {
			warnings++
		}
	}
	msg := "itervox quickstart: doctor checks passed"
	if warnings > 0 {
		msg += fmt.Sprintf(" (%d warning(s); `itervox doctor%s` lists them)", warnings, quickstartWorkflowFlag(workflowPath))
	}
	_, _ = fmt.Fprintln(out, msg)
	return 0
}

// doctorFailureLines picks the failing lines out of a rendered doctor
// report, with the indented lines that continue them (the fix commands):
// a failed schema check, ERROR lines, a startup error, the warnings that
// fail doctor (port in use, a stale heartbeat, an unreachable dashboard
// URL) and the repo-readiness rows marked fail with their fix.
func doctorFailureLines(r DoctorReport, rendered string) []string {
	var out []string
	continuing := false
	for _, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		failing := strings.HasPrefix(line, "schema: FAILED") ||
			strings.HasPrefix(line, "ERROR:") ||
			strings.HasPrefix(line, "last startup error:") ||
			strings.Contains(line, "(NOT reachable") ||
			strings.HasPrefix(trimmed, "[fail]") ||
			(r.PortInUseWarning != "" && line == "WARNING: "+r.PortInUseWarning) ||
			(r.HeartbeatStaleWarning != "" && line == "WARNING: "+r.HeartbeatStaleWarning)
		switch {
		case failing:
			out = append(out, trimmed)
			continuing = true
		case continuing && trimmed != "" && strings.HasPrefix(line, " ") && !strings.HasPrefix(trimmed, "["):
			out = append(out, "  "+trimmed)
		default:
			continuing = false
		}
	}
	return out
}
