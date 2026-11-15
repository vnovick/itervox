// Command itervox-evals is the contributor front end of the profile evals
// (#189): the Makefile's evals targets run it against the repository's
// fixtures and eval project. Released binaries ship the same commands as
// `itervox evals …`; both call internal/evals/cli.
//
//	itervox-evals recorded [flags]                 judge the committed recordings (no agent, no tokens)
//	itervox-evals live    [flags]                  run the live suite; transcripts go to -out
//	itervox-evals record  [flags]                  re-record: write recording.candidate.jsonl next to each fixture
//	itervox-evals promote [-force] -all | <profile>/<scenario>...
//	                                               replace recording.jsonl with a reviewed candidate
//
// live and record spend tokens and refuse to start unless
// ITERVOX_EVALS_LIVE=1. Recorded mode (`make evals-fast`) stays the free gate
// in `make verify`. See docs/evals.md.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	evalscli "github.com/vnovick/itervox/internal/evals/cli"
)

// defaults point at the repository: the committed fixtures (which record
// and promote write next to) and the eval project's WORKFLOW.md.
var defaults = evalscli.Defaults{
	Prog:        "itervox-evals",
	Fixtures:    "internal/evals/fixtures",
	Workflow:    "internal/evals/project/WORKFLOW.md",
	PromoteHint: "make evals-promote SCENARIO=<profile>/<scenario>` (or SCENARIO=all)",
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is main without the process exit, so tests can drive it. Exit codes:
// 0 every scenario passed, 1 a scenario failed, 2 usage or setup error.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return evalscli.Run(ctx, defaults, args, stdout, stderr)
}
