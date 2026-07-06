package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderDoctorReport_HappyPath(t *testing.T) {
	r := DoctorReport{
		Workflow:      "WORKFLOW.md",
		SchemaPassed:  true,
		RunningBinary: "/usr/local/bin/itervox",
		PathBinary:    "/usr/local/bin/itervox",
	}
	out := renderDoctorReport(r)
	if !strings.Contains(out, "schema: OK") {
		t.Errorf("missing schema OK line: %s", out)
	}
	if strings.Contains(out, "WARNING") {
		t.Error("happy path should not include WARNING")
	}
}

func TestRenderDoctorReport_BinaryDriftAsInfoWhenItervoxBinPinned(t *testing.T) {
	r := DoctorReport{
		Workflow:      "WORKFLOW.md",
		SchemaPassed:  true,
		RunningBinary: "/Users/dev/itervox/itervox",
		PathBinary:    "/opt/homebrew/bin/itervox",
		ItervoxBinEnv: "/Users/dev/itervox/itervox",
		DriftWarning:  "two itervox binaries on this machine — ITERVOX_BIN pinned",
		DriftIsError:  false,
	}
	out := renderDoctorReport(r)
	if !strings.Contains(out, "info:") {
		t.Errorf("expected info: prefix when ITERVOX_BIN pinned; got:\n%s", out)
	}
	if strings.Contains(out, "ERROR:") {
		t.Errorf("must NOT render ERROR when ITERVOX_BIN pinned; got:\n%s", out)
	}
}

func TestRenderDoctorReport_BinaryDriftAsErrorOnDevVsStable(t *testing.T) {
	r := DoctorReport{
		Workflow:      "WORKFLOW.md",
		SchemaPassed:  true,
		RunningBinary: "/Users/dev/itervox/itervox",
		PathBinary:    "/opt/homebrew/bin/itervox",
		DriftWarning:  "hooks will invoke ...",
		DriftIsError:  true,
	}
	out := renderDoctorReport(r)
	if !strings.Contains(out, "ERROR:") {
		t.Errorf("expected ERROR: prefix when dev-vs-stable drift; got:\n%s", out)
	}
}

func TestRenderDoctorReport_StartupErrorMarkerSurfaced(t *testing.T) {
	r := DoctorReport{
		Workflow:         "WORKFLOW.md",
		SchemaPassed:     true,
		StartupErrorPath: ".itervox/STARTUP_ERROR.md",
	}
	out := renderDoctorReport(r)
	if !strings.Contains(out, "last startup error") {
		t.Errorf("expected last startup error line: %s", out)
	}
}

func TestRenderStartupErrorMarker_FormatsErrorSection(t *testing.T) {
	body := renderStartupErrorMarker("WORKFLOW.md", errors.New("yaml: line 3: mapping values are not allowed"), time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(body, "yaml: line 3") {
		t.Error("startup error body missing original error")
	}
	if !strings.Contains(body, "Suggested fix") {
		t.Error("startup error body missing suggested fix")
	}
	if !strings.Contains(body, "2026-05-28") {
		t.Error("startup error body missing timestamp")
	}
}

func TestSuggestStartupErrorFix_YAMLBranch(t *testing.T) {
	got := suggestStartupErrorFix(errors.New("yaml: unmarshal errors: indent"))
	if !strings.Contains(got, "YAML is malformed") {
		t.Errorf("expected YAML branch; got %q", got)
	}
}

func TestSuggestStartupErrorFix_SchemaBranch(t *testing.T) {
	got := suggestStartupErrorFix(errors.New("WORKFLOW.md is missing itervox_schema_version"))
	if !strings.Contains(got, "init --update") {
		t.Errorf("expected schema-upgrade hint; got %q", got)
	}
}

func TestWriteAndClearStartupErrorMarker_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	writeStartupErrorMarker(wf, errors.New("test failure"))
	marker := filepath.Join(tmp, ".itervox", "STARTUP_ERROR.md")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected %s to exist: %v", marker, err)
	}
	clearStartupErrorMarker(wf)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("expected marker to be cleared; got %v", err)
	}
}

func TestRunDoctorChecks_ExitsNonZeroOnInvalidWorkflow(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "BAD_WORKFLOW.md")
	if err := os.WriteFile(wf, []byte("---\n: : not yaml\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, code := runDoctorChecks(wf, os.Stdout)
	if code == 0 {
		t.Error("expected non-zero exit code for invalid workflow")
	}
}

func TestRenderDoctorReport_GitignoreMissingLines(t *testing.T) {
	r := DoctorReport{
		Workflow:              "WORKFLOW.md",
		SchemaPassed:          true,
		GitignoreMissingLines: []string{"daemon.pid", "*.db"},
	}
	out := renderDoctorReport(r)
	if !strings.Contains(out, "WARNING:") {
		t.Errorf("expected WARNING for missing gitignore lines: %s", out)
	}
	if !strings.Contains(out, "daemon.pid") || !strings.Contains(out, "*.db") {
		t.Errorf("expected missing lines in warning output: %s", out)
	}
	if !strings.Contains(out, "init --update") {
		t.Errorf("expected init --update suggestion: %s", out)
	}
}
