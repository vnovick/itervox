package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vnovick/itervox/internal/depsanalysis"
)

// runDeps is the entrypoint for `itervox deps <subcommand>`.
//
// Subcommands:
//
//	analyze [--workflow PATH] [--full]
//	    Run one synchronous dependency-analysis pass and write
//	    .itervox/dependencies.json. Same code path init takes on first run.
//	    Use when the daemon is NOT running (no HTTP endpoint to POST to).
//	    Incremental by default (only new/changed issues go through the
//	    agent); --full forces a full re-analysis pass.
//	list [--workflow PATH]
//	    Print the current .itervox/dependencies.json as JSON.
//
// The daemon-driven equivalent is `POST /api/v1/deps/analyze` (Settings → Deps
// graph → "Analyze dependencies" button). The CLI gives operators the same
// capability from a shell — useful for CI scripts, scheduled refreshes, and
// situations where the dashboard is not running.
func runDeps(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: itervox deps <analyze|list> [flags]")
		fatalExit(2)
	}
	switch args[0] {
	case "analyze":
		runDepsAnalyze(args[1:])
	case "list":
		runDepsList(args[1:])
	case "-h", "--help":
		fmt.Print(`usage: itervox deps <subcommand> [flags]

Subcommands:
  analyze   Run one synchronous dependency-analysis pass and write
            .itervox/dependencies.json. Same pipeline as ` + "`itervox init`" + `.
  list      Print the current sidecar (.itervox/dependencies.json) as JSON.

Flags:
  --workflow PATH   Path to WORKFLOW.md (default: WORKFLOW.md)
  --full            (analyze only) force a full re-analysis pass, ignoring
                     the incremental fingerprint sidecar
`)
	default:
		fmt.Fprintf(os.Stderr, "itervox deps: unknown subcommand %q\n", args[0])
		fatalExit(2)
	}
}

func runDepsAnalyze(args []string) {
	fs := flag.NewFlagSet("deps analyze", flag.ExitOnError)
	workflowPath := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md")
	full := fs.Bool("full", false, "force a full re-analysis pass, ignoring the incremental fingerprint sidecar")
	_ = fs.Parse(args)

	mode := "auto"
	if *full {
		mode = depsanalysis.IncrementalModeFull
	}

	warnIfDaemonRunning(*workflowPath, "`itervox deps analyze`")

	loadDotEnv()
	issueCount, analyzedCount, edgeCount, sidecarPath, guarded, err := runInitDepsAnalysis(*workflowPath, mode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "itervox deps analyze: %v\n", err)
		fatalExit(1)
	}
	if guarded {
		fmt.Printf("itervox deps analyze: fetch returned no issues; refusing to overwrite %d inferred edge(s) already in %s (sidecar left unchanged)\n",
			edgeCount, sidecarPath)
		return
	}
	// #52 IssuesScanned honesty — issueCount is the raw fetch count;
	// analyzedCount is what actually went to the agent (revalidated =
	// scanned but unchanged, carried forward without a fresh agent pass).
	revalidatedCount := issueCount - analyzedCount
	fmt.Printf("itervox deps analyze: scanned %d issue(s) (%d analyzed, %d revalidated); inferred %d edge(s) (wrote %s)\n",
		issueCount, analyzedCount, revalidatedCount, edgeCount, sidecarPath)
}

func runDepsList(args []string) {
	fs := flag.NewFlagSet("deps list", flag.ExitOnError)
	workflowPath := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md")
	_ = fs.Parse(args)

	dir := filepath.Dir(*workflowPath)
	if dir == "" {
		dir = "."
	}
	sc, err := depsanalysis.LoadSidecar(depsanalysis.SidecarPath(dir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "itervox deps list: %v\n", err)
		fatalExit(1)
	}
	if sc == nil {
		fmt.Println(`{"edges":[]}`)
		fmt.Fprintln(os.Stderr,
			`itervox deps list: no sidecar yet — run "itervox deps analyze" or "Analyze dependencies" from the dashboard`)
		return
	}
	out, _ := json.MarshalIndent(sc, "", "  ")
	fmt.Println(string(out))
}
