// Package referee keeps coding agents in CI from changing how CI checks
// their work. An agent may change what CI checks, never how CI checks.
//
// It runs outside the branch under review, reads its policy from the base
// branch, and the writer is never the judge:
//
//	lanes   classify a diff into human / second_opinion / auto
//	audit   list changes that weaken checks (skips, deleted tests, …)
//	envcheck  fail when an installed tool reads a deploy-target variable
//	          its guard does not classify
//	diff    run each tool's probes on the base and head installs and
//	          report how its behaviour changed
//	pin     fail when a policy file's sha256 is not the pinned one
//	run     run a check and append a signed record of what it saw
//	keygen  make the ed25519 key pair records are signed with
//	verify  verify signed records (and that the tree still matches)
//
// It works with any agent harness and any CI; it needs only git. Two
// front ends share it: `itervox referee …` (shipped in every release) and
// the standalone cmd/referee binary (`go install`, the referee Action).
package referee

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Exit codes. An error is never 0: referee fails closed.
const (
	exitOK            = 0
	exitFailed        = 1 // run: the check failed; verify: a record did not verify
	exitError         = 2 // usage, I/O or git error
	exitSecondOpinion = 10
	exitHuman         = 20 // lanes: human lane; audit, envcheck: findings; pin: mismatch
)

const usageTemplate = `usage: {prog} <command> [flags]

An agent may change what CI checks, never how CI checks.

commands:
  lanes    classify the diff against a base ref (policy read from the base ref)
  audit    list changes that weaken checks
  envcheck fail when an installed tool reads a deploy-target variable its guard
           does not classify (tools: in the base ref's policy)
  diff     run each tool's probes on the base install (--base-dir) and the head
           install and report behaviour changes (exit 10)
  pin     check a policy file against a pinned sha256 (or --print it)
  run      run a check and append a signed record ({prog} run --name N --key K --out F -- cmd...)
  keygen   make an ed25519 key pair ({prog} keygen --out DIR)
  verify   verify signed records ({prog} verify F --pubkey P [--tree DIR])

Run "{prog} <command> -h" for a command's flags.
Exit codes: 0 ok/auto, 1 check failed or record did not verify,
10 second_opinion or a diff behaviour change, 20 human lane, audit or envcheck findings or pin mismatch,
2 usage or error (never 0).`

// envFunc reads an environment variable (os.Getenv; tests inject one).
type envFunc func(string) string

// Run dispatches a referee subcommand and returns the process exit code.
// prog is how the user invoked it ("referee" or "itervox referee") and is
// only used in the top-level usage text. The caller owns process setup
// (gitexec.UnsetInProcess, logging) and the exit itself.
func Run(ctx context.Context, prog string, args []string, env func(string) string, stdout, stderr io.Writer) int {
	return dispatch(ctx, prog, args, env, stdout, stderr)
}

func dispatch(ctx context.Context, prog string, args []string, env envFunc, stdout, stderr io.Writer) int {
	usage := strings.ReplaceAll(usageTemplate, "{prog}", prog)
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return exitError
	}
	rest := args[1:]
	switch args[0] {
	case "lanes":
		return lanesCmd(ctx, rest, env, stdout, stderr)
	case "audit":
		return auditCmd(ctx, rest, env, stdout, stderr)
	case "envcheck":
		return envcheckCmd(ctx, rest, env, stdout, stderr)
	case "diff":
		return diffCmd(ctx, rest, env, stdout, stderr)
	case "pin":
		return pinCmd(rest, stdout, stderr)
	case "run":
		return runCmd(ctx, rest, stdout, stderr)
	case "keygen":
		return keygenCmd(rest, stdout, stderr)
	case "verify":
		return verifyCmd(ctx, rest, stdout, stderr)
	case "-h", "--help", "help":
		_, _ = fmt.Fprintln(stdout, usage)
		return exitOK
	}
	_, _ = fmt.Fprintf(stderr, "referee: unknown command %q\n\n%s\n", args[0], usage)
	return exitError
}

// resolveBaseFlag is --base, or origin/$GITHUB_BASE_REF in a pull_request
// workflow. No base is a usage error: guessing one could compare against
// the wrong branch.
func resolveBaseFlag(base string, env envFunc) (string, error) {
	if base = strings.TrimSpace(base); base != "" {
		return base, nil
	}
	if ref := strings.TrimSpace(env("GITHUB_BASE_REF")); ref != "" {
		return "origin/" + ref, nil
	}
	return "", errors.New("--base is required (the branch the change merges into, e.g. origin/main)")
}

func usageError(stderr io.Writer, usageText, format string, a ...any) int {
	_, _ = fmt.Fprintf(stderr, "referee: "+format+"\n\n%s\n", append(a, usageText)...)
	return exitError
}

func failf(stderr io.Writer, cmd, format string, a ...any) int {
	_, _ = fmt.Fprintf(stderr, "referee %s: "+format+"\n", append([]any{cmd}, a...)...)
	return exitError
}
