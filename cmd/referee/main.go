// Command referee is the standalone build of `itervox referee`: the same
// code (internal/referee) for projects that install only the referee, e.g.
// with `brew install vnovick/tap/referee`, `go install` or the referee
// GitHub Action.
//
// An agent may change what CI checks, never how CI checks.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/referee"
)

// Set by the release build via -ldflags "-X main.version=… -X main.commit=…
// -X main.date=…" (.goreleaser.yaml, build id referee), like cmd/itervox.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	gitexec.UnsetInProcess()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run handles the binary's own flags (--version) and hands everything else
// to referee.Run, which owns the subcommands, help and exit codes.
func run(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "--version", "-version", "version":
			_, _ = fmt.Fprintf(stdout, "referee %s (commit: %s, built: %s, ruleset: %s)\n", version, commit, date, referee.RulesetVersion())
			return 0
		}
	}
	referee.SetBuild(version, commit) // named in the signed referee record
	return referee.Run(ctx, "referee", args, env, stdout, stderr)
}
