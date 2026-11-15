// Command referee is the standalone build of `itervox referee`: the same
// code (internal/referee) for projects that install only the referee, e.g.
// with `go install` or the referee GitHub Action.
//
// An agent may change what CI checks, never how CI checks.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/referee"
)

func main() {
	gitexec.UnsetInProcess()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	exit(referee.Run(context.Background(), "referee", os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}
