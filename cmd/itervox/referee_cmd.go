package main

import (
	"context"
	"io"
	"os"

	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/referee"
)

// runRefereeEntrypoint runs `itervox referee …`, the CI referee shipped in
// every release (the same code as the standalone cmd/referee). It runs
// before any daemon start-up work and, deliberately, before loadDotEnv:
// `referee run` executes the branch's own checks, and those must never
// inherit secrets from a .itervox/.env in the tree being judged.
func runRefereeEntrypoint(args []string) {
	if len(args) > 1 && args[1] == "referee" {
		gitexec.UnsetInProcess()
		exitWithoutCleanup(refereeCmd(context.Background(), args[2:], os.Getenv, os.Stdout, os.Stderr))
	}
}

func refereeCmd(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer) int {
	return referee.Run(ctx, "itervox referee", args, env, stdout, stderr)
}
