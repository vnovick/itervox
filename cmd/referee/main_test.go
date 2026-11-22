package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/referee"
)

// TestRefereeRecordUsesLdflagsValues: the signed referee record names the
// version and commit the release build stamps (main.version, main.commit).
func TestRefereeRecordUsesLdflagsValues(t *testing.T) {
	gitexec.UnsetInProcess()
	oldV, oldC := version, commit
	t.Cleanup(func() { version, commit = oldV, oldC })
	version, commit = "0.3.4", "fedcba9876543210fedcba9876543210fedcba98"

	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.email", "referee@example.invalid"},
		{"config", "user.name", "referee test"}, {"config", "commit.gpgsign", "false"},
		{"commit", "-q", "--allow-empty", "-m", "base"},
	} {
		if out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	keys, records := t.TempDir(), filepath.Join(t.TempDir(), "referee.jsonl")
	env := func(string) string { return "" }
	for _, args := range [][]string{
		{"keygen", "--out", keys},
		{"audit", "--dir", dir, "--base", "main", "--key", filepath.Join(keys, attest.KeyFileName), "--record", records},
	} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), args, env, &out, &errOut); code != 0 {
			t.Fatalf("%v: exit %d: %s%s", args, code, out.String(), errOut.String())
		}
	}
	recs, err := attest.ReadFile(records)
	if err != nil || len(recs) != 1 || recs[0].Referee == nil {
		t.Fatalf("records %+v, err %v", recs, err)
	}
	if got := recs[0].Referee; got.Version != version || got.Commit != commit || got.Command != "audit" {
		t.Fatalf("referee record %+v, want version %s commit %s command audit", got, version, commit)
	}
}

func TestRefereeVersionFlags(t *testing.T) {
	for _, arg := range []string{"--version", "-version", "version"} {
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{arg}, func(string) string { return "" }, &out, &errOut)
		if code != 0 {
			t.Fatalf("%s: exit %d, want 0 (stderr %q)", arg, code, errOut.String())
		}
		want := "referee dev (commit: none, built: unknown, ruleset: " + referee.RulesetVersion() + ")\n"
		if out.String() != want {
			t.Fatalf("%s: stdout %q, want %q", arg, out.String(), want)
		}
		if errOut.Len() != 0 {
			t.Fatalf("%s: unexpected stderr %q", arg, errOut.String())
		}
	}
}

func TestRefereeVersionUsesLdflagsValues(t *testing.T) {
	oldV, oldC, oldD := version, commit, date
	t.Cleanup(func() { version, commit, date = oldV, oldC, oldD })
	version, commit, date = "0.3.3", "abc1234", "2026-11-15T00:00:00Z"
	var out bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, func(string) string { return "" }, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if got, want := out.String(), "referee 0.3.3 (commit: abc1234, built: 2026-11-15T00:00:00Z, ruleset: "+referee.RulesetVersion()+")\n"; got != want {
		t.Fatalf("stdout %q, want %q", got, want)
	}
}

// The binary's own flags must not shadow referee.Run's behaviour: help,
// no-args and unknown commands keep their exit codes.
func TestRefereeDelegatesToRun(t *testing.T) {
	env := func(string) string { return "" }
	cases := []struct {
		args []string
		code int
		out  string // substring expected on stdout
		err  string // substring expected on stderr
	}{
		{args: nil, code: 2, err: "usage: referee <command>"},
		{args: []string{"help"}, code: 0, out: "usage: referee <command>"},
		{args: []string{"--help"}, code: 0, out: "usage: referee <command>"},
		{args: []string{"nope"}, code: 2, err: `unknown command "nope"`},
	}
	for _, tc := range cases {
		var out, errOut bytes.Buffer
		code := run(context.Background(), tc.args, env, &out, &errOut)
		if code != tc.code {
			t.Fatalf("%v: exit %d, want %d", tc.args, code, tc.code)
		}
		if tc.out != "" && !strings.Contains(out.String(), tc.out) {
			t.Fatalf("%v: stdout %q lacks %q", tc.args, out.String(), tc.out)
		}
		if tc.err != "" && !strings.Contains(errOut.String(), tc.err) {
			t.Fatalf("%v: stderr %q lacks %q", tc.args, errOut.String(), tc.err)
		}
	}
}
