package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// `itervox doctor` says that sessions.terminal_write does nothing on
// Windows (no tmux, no live terminal), without failing; on other
// platforms, or with the setting off, it says nothing.
func TestDoctorWarnsTerminalWriteOnWindows(t *testing.T) {
	old := doctorGOOS
	t.Cleanup(func() { doctorGOOS = old })
	on := writeConfigWatchWorkflow(t, strings.Replace(configWatchFixture, "agent:\n", "sessions:\n  terminal_write: true\nagent:\n", 1))
	off := writeConfigWatchWorkflow(t, configWatchFixture)
	const line = "WARNING: sessions.terminal_write has no effect on Windows: the live terminal needs tmux"

	doctorGOOS = "windows"
	report, cfg := collectDoctorReport(on)
	assert.True(t, cfg.Sessions.TerminalWrite)
	assert.Contains(t, renderDoctorReport(report), line)
	assert.Equal(t, doctorExitCode(DoctorReport{SchemaPassed: true}), doctorExitCode(DoctorReport{SchemaPassed: true, TerminalWriteWarning: report.TerminalWriteWarning}),
		"a notice, never a failure")

	report, _ = collectDoctorReport(off)
	assert.NotContains(t, renderDoctorReport(report), "sessions.terminal_write")

	doctorGOOS = "linux"
	report, _ = collectDoctorReport(on)
	assert.NotContains(t, renderDoctorReport(report), "sessions.terminal_write")
}
