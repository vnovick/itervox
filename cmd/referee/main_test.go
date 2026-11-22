package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRefereeVersionFlags(t *testing.T) {
	for _, arg := range []string{"--version", "-version", "version"} {
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{arg}, func(string) string { return "" }, &out, &errOut)
		if code != 0 {
			t.Fatalf("%s: exit %d, want 0 (stderr %q)", arg, code, errOut.String())
		}
		want := "referee dev (commit: none, built: unknown)\n"
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
	if got, want := out.String(), "referee 0.3.3 (commit: abc1234, built: 2026-11-15T00:00:00Z)\n"; got != want {
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

// TestRefereeHelpListsEnvcheck: the standalone binary's usage names the
// envcheck command, and `envcheck -h` reaches its own usage.
func TestRefereeHelpListsEnvcheck(t *testing.T) {
	env := func(string) string { return "" }
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"help"}, env, &out, &errOut); code != 0 {
		t.Fatalf("help: exit %d", code)
	}
	if !strings.Contains(out.String(), "envcheck") {
		t.Fatalf("usage lacks envcheck: %q", out.String())
	}
	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), []string{"envcheck", "-h"}, env, &out, &errOut); code != 2 {
		t.Fatalf("envcheck -h: exit %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage: referee envcheck --base <ref>") {
		t.Fatalf("envcheck -h: stderr %q", errOut.String())
	}
}
