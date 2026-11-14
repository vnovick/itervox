// Command referee keeps coding agents in CI from changing how CI checks
// their work. An agent may change what CI checks, never how CI checks.
//
// It runs outside the branch under review, reads its policy from the base
// branch, and the writer is never the judge:
//
//	referee lanes   classify a diff into human / second_opinion / auto
//	referee audit   list changes that weaken checks (skips, deleted tests, …)
//	referee pin     fail when a policy file's sha256 is not the pinned one
//	referee run     run a check and append a signed record of what it saw
//	referee keygen  make the ed25519 key pair records are signed with
//	referee verify  verify signed records (and that the tree still matches)
//
// It works with any agent harness and any CI; it needs only git.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/vnovick/itervox/internal/gitexec"
)

// Exit codes. An error is never 0: referee fails closed.
const (
	exitOK            = 0
	exitFailed        = 1 // run: the check failed; verify: a record did not verify
	exitError         = 2 // usage, I/O or git error
	exitSecondOpinion = 10
	exitHuman         = 20 // lanes: human lane; audit: findings; pin: mismatch
)

const usage = `usage: referee <command> [flags]

An agent may change what CI checks, never how CI checks.

commands:
  lanes    classify the diff against a base ref (policy read from the base ref)
  audit    list changes that weaken checks
  pin      check a policy file against a pinned sha256 (or --print it)
  run      run a check and append a signed record (referee run --name N --key K --out F -- cmd...)
  keygen   make an ed25519 key pair (referee keygen --out DIR)
  verify   verify signed records (referee verify F --pubkey P [--tree DIR])

Run "referee <command> -h" for a command's flags.
Exit codes: 0 ok/auto, 1 check failed or record did not verify,
10 second_opinion, 20 human lane, audit findings or pin mismatch,
2 usage or error (never 0).`

// envFunc reads an environment variable (os.Getenv; tests inject one).
type envFunc func(string) string

func main() {
	gitexec.UnsetInProcess()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run dispatches a subcommand and returns the process exit code.
func run(ctx context.Context, args []string, env envFunc, stdout, stderr io.Writer) int {
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
