package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vnovick/itervox/internal/config"
)

// ensureEnvStub writes .itervox/.env when it is absent, seeded with the
// variable the given tracker kind needs.
//
// Idempotent and never overwrites: an operator's real credentials must
// survive a re-run. An unknown/empty kind writes both stubs commented-free is
// not possible, so it writes nothing rather than guessing wrong.
func ensureEnvStub(itervoxDir, trackerKind string) {
	if itervoxDir == "" {
		return
	}
	envPath := filepath.Join(itervoxDir, ".env")
	if _, err := os.Stat(envPath); err == nil {
		return // already present — never clobber real credentials
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "itervox init: stat %s: %v\n", envPath, err)
		return
	}
	const header = "# Itervox environment — this file is gitignored.\n# See WORKFLOW.md for which variables are referenced.\n"
	var envContent string
	switch trackerKind {
	case "linear":
		envContent = header + "LINEAR_API_KEY=lin_api_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"
	case "github":
		envContent = header + "GITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"
	default:
		// Unknown tracker: still create the file with the header so the
		// operator has an obvious place to put credentials, rather than
		// leaving them to discover the path from an error message.
		envContent = header
	}
	if err := os.MkdirAll(itervoxDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "itervox init: create %s: %v\n", itervoxDir, err)
		return
	}
	if err := os.WriteFile(envPath, []byte(envContent), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "itervox init: write %s: %v\n", envPath, err)
		return
	}
	fmt.Printf("itervox init: wrote %s — fill in your credentials before starting the daemon\n", envPath)
}

// trackerKindFromWorkflow reads tracker.kind out of an existing WORKFLOW.md so
// --update can seed the right variable. Returns "" when it cannot be
// determined, which ensureEnvStub handles by writing just the header.
func trackerKindFromWorkflow(workflowPath string) string {
	cfg, err := config.Load(workflowPath)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Tracker.Kind
}
