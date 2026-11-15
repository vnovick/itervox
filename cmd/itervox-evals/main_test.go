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

func TestEvalsCLILiveRefusesWithoutOptIn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	fake := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(evals.LiveOptInEnv, "")
	for _, cmd := range []string{"live", "record"} {
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{cmd, "-fixtures", "../../internal/evals/fixtures",
			"-workflow", "../../internal/evals/project/WORKFLOW.md", "-command", fake}, &out, &errOut)
		if code != 2 || !strings.Contains(errOut.String(), evals.LiveOptInEnv+"=1") {
			t.Errorf("%s without opt-in: want exit 2 naming %s; got %d %q", cmd, evals.LiveOptInEnv, code, errOut.String())
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("no backend may start without the opt-in")
	}
}

func TestEvalsCLIPromote(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "reviewer", "approves")
	files := map[string]string{
		"input.yaml":        "issue: {identifier: ENG-1, branch_name: b}\n",
		"expected.yaml":     "required_action_calls:\n  - comment\nforbidden_phrases:\n  - \"review failed\"\n",
		"recording.jsonl":   `{"event":"action","action":"comment"}` + "\n",
		evals.CandidateName: `{"event":"meta","source":"live"}` + "\n" + `{"event":"action","action":"comment"}` + "\n",
	}
	for name, content := range files {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"promote", "-fixtures", root}, &out, &errOut); code != 2 {
		t.Errorf("promote without a target must be a usage error; got %d", code)
	}
	if code := run(context.Background(), []string{"promote", "-fixtures", root, "reviewer/approves"}, &out, &errOut); code != 0 {
		t.Fatalf("promote: exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "[evals] 1/1 pass") {
		t.Errorf("promote must replay the promoted fixture in recorded mode; stdout:\n%s", out.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "recording.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != files[evals.CandidateName] {
		t.Errorf("recording.jsonl must be the promoted candidate; got %q", got)
	}
	if code := run(context.Background(), []string{"promote", "-fixtures", root, "-all"}, &out, &errOut); code != 2 {
		t.Errorf("promote -all with no candidates left must fail; got %d", code)
	}

	// A forced promotion of a failing candidate lands, but the recorded-mode
	// replay reports it and the command exits 1.
	failing := `{"event":"comment","body":"AI review failed"}` + "\n" + `{"event":"action","action":"comment"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, evals.CandidateName), []byte(failing), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run(context.Background(), []string{"promote", "-fixtures", root, "reviewer/approves"}, &out, &errOut); code != 1 {
		t.Errorf("a failing candidate must be refused with exit 1; got %d", code)
	}
	if code := run(context.Background(), []string{"promote", "-fixtures", root, "-force", "-all"}, &out, &errOut); code != 1 || !strings.Contains(out.String(), "forbidden phrase present in comments: review failed") {
		t.Errorf("forced promotion must surface the failing replay and exit 1; got %d:\n%s", code, out.String())
	}
}

// TestEvalsCLIRecordedSavesReport (#201): `itervox-evals recorded` (what
// `make evals` runs) judges the committed recordings and saves the report
// as JSON under -results-dir; -results-dir "" saves nothing.
func TestEvalsCLIRecordedSavesReport(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evals")
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"recorded", "-fixtures", "../../internal/evals/fixtures", "-results-dir", dir}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out.String(), errOut.String())
	}
	sum, err := evals.LoadResults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Reports) != 1 || sum.Reports[0].Mode != evals.ModeRecorded || sum.Reports[0].Total == 0 || sum.Reports[0].Pass != sum.Reports[0].Total {
		t.Fatalf("saved report = %+v", sum.Reports)
	}
	if !strings.Contains(out.String(), "[evals] report saved to "+filepath.Join(dir, sum.Reports[0].File)) {
		t.Errorf("the CLI names the saved file; got %q", out.String())
	}

	fixtures, err := filepath.Abs("../../internal/evals/fixtures")
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	t.Chdir(cwd)
	if code := run(context.Background(), []string{"recorded", "-fixtures", fixtures, "-results-dir", "", "-profile", "reviewer"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf(`-results-dir "" must not save; the working dir has %v`, entries)
	}
}
