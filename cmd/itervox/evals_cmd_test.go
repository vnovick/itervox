package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/evals"
)

// TestItervoxEvalsRecordedUsesEmbeddedFixtures: in a project with no
// fixtures of its own (and no repository checkout), `itervox evals
// recorded` judges the built-in scenarios embedded in the binary and saves
// the report under .itervox/evals, where the dashboard reads it.
func TestItervoxEvalsRecordedUsesEmbeddedFixtures(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	var out, errOut bytes.Buffer
	if code := evalsCmd(context.Background(), []string{"recorded"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d:\n%s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "[evals] fixtures: built-in scenarios") {
		t.Errorf("the command names where the scenarios came from; stderr %q", errOut.String())
	}
	sum, err := evals.LoadResults(filepath.Join(project, evals.ResultsDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Reports) != 1 || sum.Reports[0].Mode != evals.ModeRecorded || sum.Reports[0].Total < 8 || sum.Reports[0].Pass != sum.Reports[0].Total {
		t.Fatalf("saved report = %+v", sum.Reports)
	}
	if !strings.Contains(out.String(), "[evals] report saved to "+filepath.Join(evals.ResultsDir, sum.Reports[0].File)) {
		t.Errorf("stdout must name the saved file; got %q", out.String())
	}
	entries, err := os.ReadDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ".itervox" {
		t.Errorf("only .itervox/evals may be written into the project; got %v", entries)
	}
}

// TestItervoxEvalsProjectFixturesAndWorkflowDir: -workflow names the
// project; its .itervox/evals/fixtures wins over the built-in scenarios and
// the report lands next to that WORKFLOW.md.
func TestItervoxEvalsProjectFixturesAndWorkflowDir(t *testing.T) {
	project := filepath.Join(t.TempDir(), "proj")
	dir := filepath.Join(project, ".itervox", "evals", "fixtures", "reviewer", "approves")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"input.yaml":      "issue: {identifier: ENG-1, branch_name: b}\n",
		"expected.yaml":   "required_action_calls:\n  - comment\n",
		"recording.jsonl": `{"event":"action","action":"comment"}` + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(t.TempDir())
	var out, errOut bytes.Buffer
	code := evalsCmd(context.Background(), []string{"recorded", "-workflow", filepath.Join(project, "WORKFLOW.md")}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d:\n%s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "[evals] 1/1 pass") || !strings.Contains(out.String(), "reviewer/approves") {
		t.Errorf("the project's own scenario must be the one judged; stdout:\n%s", out.String())
	}
	sum, err := evals.LoadResults(filepath.Join(project, evals.ResultsDir))
	if err != nil || len(sum.Reports) != 1 {
		t.Fatalf("report next to WORKFLOW.md: %+v, %v", sum.Reports, err)
	}
}

// TestItervoxEvalsLiveRefusesWithoutOptIn: live and record never start an
// agent without ITERVOX_EVALS_LIVE=1, whatever the fixtures.
func TestItervoxEvalsLiveRefusesWithoutOptIn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	fake := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	t.Setenv(evals.LiveOptInEnv, "")
	for _, cmd := range []string{"live", "record"} {
		var out, errOut bytes.Buffer
		code := evalsCmd(context.Background(), []string{cmd, "-command", fake}, &out, &errOut)
		if code != 2 || !strings.Contains(errOut.String(), evals.LiveOptInEnv+"=1") {
			t.Errorf("%s without opt-in: want exit 2 naming %s; got %d %q", cmd, evals.LiveOptInEnv, code, errOut.String())
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("no backend may start without the opt-in")
	}
}

// TestItervoxEvalsRecordAndPromoteRefuseBuiltinFixtures: record and promote
// write next to the fixtures, so with neither -fixtures nor project
// fixtures they refuse (before any agent starts) and say what to pass.
func TestItervoxEvalsRecordAndPromoteRefuseBuiltinFixtures(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	fake := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	t.Setenv(evals.LiveOptInEnv, "1")
	for _, args := range [][]string{{"record", "-command", fake}, {"promote", "-all"}} {
		var out, errOut bytes.Buffer
		code := evalsCmd(context.Background(), args, &out, &errOut)
		if code != 2 || !strings.Contains(errOut.String(), "pass -fixtures DIR") {
			t.Errorf("%v: want exit 2 asking for -fixtures; got %d %q", args, code, errOut.String())
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("record must refuse before any backend starts")
	}
}
