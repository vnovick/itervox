package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// runStatus lists all itervox daemons associated with the current project
// directory. Non-intrusive counterpart to `itervox stop` — useful when the
// user wants to see what's running before killing anything, or when
// troubleshooting "why is port 8090 already in use?".
//
// Scope:
//   - Default: only daemons whose cwd matches this project directory or
//     whose PID file is at <workflowDir>/.itervox/daemon.pid.
//   - `--all`: also list any other itervox daemons on the system,
//     regardless of cwd. Handy when the user has multiple repos open and
//     wants a global view.
func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	workflowPath := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md for the project to inspect")
	all := fs.Bool("all", false, "list itervox daemons across all projects, not just this one")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: itervox status [flags]

Lists running itervox daemons.

Flags:
`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	projectDir, _ := resolveProjectDir(*workflowPath)

	// Local PID file (if present + alive).
	var fromFile []int
	if pid, _, _, err := readPIDFile(*workflowPath); err == nil && processAlive(pid) {
		fromFile = append(fromFile, pid)
	}

	// Matching cwd (the scoped view).
	fromScan := discoverDaemonsByCwd(projectDir)

	// Everything else (only when --all).
	var extras []int
	if *all {
		for _, pid := range discoverAllItervoxDaemons() {
			if !slices.Contains(fromFile, pid) && !slices.Contains(fromScan, pid) {
				extras = append(extras, pid)
			}
		}
	}

	// Union for the scoped section.
	scoped := append([]int{}, fromFile...)
	for _, pid := range fromScan {
		if !slices.Contains(scoped, pid) {
			scoped = append(scoped, pid)
		}
	}

	absPath, _ := filepath.Abs(*workflowPath)
	fmt.Printf("project: %s\n", absPath)
	if len(scoped) == 0 {
		fmt.Println("  no daemons running for this project")
	} else {
		fmt.Printf("  %d daemon(s) running for this project:\n", len(scoped))
		for _, pid := range scoped {
			fmt.Printf("    pid %d  cwd=%s\n", pid, processCwd(pid))
		}
	}

	if *all {
		if len(extras) == 0 {
			fmt.Println("other projects: none")
		} else {
			fmt.Printf("other projects: %d daemon(s)\n", len(extras))
			for _, pid := range extras {
				fmt.Printf("  pid %d  cwd=%s\n", pid, processCwd(pid))
			}
		}
	}
}

// discoverAllItervoxDaemons delegates to the dedicated helper in
// discover.go. Named thinly here so status.go reads end-to-end without
// jumping files.
func discoverAllItervoxDaemons() []int {
	return discoverAllDaemons()
}
