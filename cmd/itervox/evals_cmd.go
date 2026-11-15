package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	evalscli "github.com/vnovick/itervox/internal/evals/cli"
)

// evalsDefaults is `itervox evals …` (the same code as the contributor tool
// cmd/itervox-evals): the project is the directory holding -workflow, the
// scenarios are -fixtures, else .itervox/evals/fixtures there, else the
// built-in ones embedded in the binary, and reports go to
// <project>/.itervox/evals, where the dashboard's Repo Analysis page reads
// them. It is dispatched after loadDotEnv on purpose: live mode runs a real
// agent CLI, which needs the API keys a project keeps in .itervox/.env.
var evalsDefaults = evalscli.Defaults{
	Prog:     "itervox evals",
	Workflow: "WORKFLOW.md",
	Project:  true,
}

func runEvals(args []string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := evalsCmd(ctx, args, os.Stdout, os.Stderr)
	stop()
	fatalExit(code)
}

func evalsCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return evalscli.Run(ctx, evalsDefaults, args, stdout, stderr)
}
