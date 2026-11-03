package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/workflow"
)

// initUpdate is `itervox init --update`: the schema-2 migration (profile
// prompts to files, the schema marker, the guardrails block), the optional
// server.port rewrite, then (#239) the move of the layered front-matter keys
// to .itervox/settings.jsonc. With dryRun nothing is written: the move is
// planned and printed.
func initUpdate(workflowPath string, force, dryRun bool, serverPort int, now time.Time, out, errOut io.Writer) error {
	if dryRun {
		if wf, err := workflow.Load(workflowPath); err == nil {
			if v, _ := wf.Config["itervox_schema_version"].(int); v != config.LatestWorkflowSchemaVersion {
				_, _ = fmt.Fprintf(out, "itervox init --update: %s is not on schema %d yet; without --dry-run the schema migration runs first (profile prompts move to .itervox/agents/)\n", workflowPath, config.LatestWorkflowSchemaVersion)
			}
		}
		move, err := moveFrontMatterToSettings(workflowPath, force, true, false, nil)
		if err != nil {
			return err
		}
		printSettingsMove(out, workflowPath, move, true)
		return nil
	}

	// The workflow as the operator left it: the backup the settings move
	// writes when the schema step wrote none.
	original, err := os.ReadFile(workflowPath)
	if err != nil {
		return err
	}
	result, err := migrateWorkflowToSchema2(workflowPath, force, now)
	if err != nil {
		return err
	}
	if result.Changed {
		_, _ = fmt.Fprintf(out, "itervox init --update: migrated %s to schema %d\n", workflowPath, config.LatestWorkflowSchemaVersion)
		// gaps_11 G-19 — tell the operator the .bak is disposable once
		// the migration is confirmed, so stale backups don't linger.
		_, _ = fmt.Fprintf(out, "itervox init --update: backup written to %s — review the migrated workflow, then remove the backup once the migration is confirmed\n", result.BackupPath)
		if len(result.Profiles) > 0 {
			_, _ = fmt.Fprintf(out, "itervox init --update: migrated profiles: %s\n", strings.Join(result.Profiles, ", "))
		}
	} else {
		_, _ = fmt.Fprintf(out, "itervox init --update: %s already uses schema %d\n", workflowPath, config.LatestWorkflowSchemaVersion)
	}
	if result.AddedGuardrails {
		_, _ = fmt.Fprintf(out, "itervox init --update: added the guardrails: block (#204) with its defaults to %s\n", workflowPath)
	}
	if serverPort >= 0 {
		if err := rewriteServerPort(workflowPath, serverPort); err != nil {
			return fmt.Errorf("rewrite server.port: %w", err)
		}
		_, _ = fmt.Fprintf(out, "itervox init --update: server.port set to %d in %s\n", serverPort, workflowPath)
	}
	for _, warning := range result.Warnings {
		_, _ = fmt.Fprintf(errOut, "itervox init --update: warning: %s\n", warning)
	}

	// #239: the layered settings leave the front matter. The schema step's
	// backup (when it wrote one) already holds the original.
	move, err := moveFrontMatterToSettings(workflowPath, force, false, result.BackupPath != "", original)
	if err != nil {
		return fmt.Errorf("move settings to %s: %w", config.ProjectSettingsPath(workflowPath), err)
	}
	printSettingsMove(out, workflowPath, move, false)
	if move.changed() && result.BackupPath == "" {
		_, _ = fmt.Fprintf(out, "itervox init --update: backup written to %s — remove it once the move is confirmed\n", move.BackupPath)
	}
	if serverPort >= 0 && slices.Contains(move.Dropped, "server.port") {
		// The settings file already set server.port, so its value was kept;
		// --server-port asked for this one.
		if err := config.SetSetting(move.SettingsPath, "server.port", serverPort); err != nil {
			return fmt.Errorf("set server.port: %w", err)
		}
		_, _ = fmt.Fprintf(out, "itervox init --update: server.port set to %d in %s\n", serverPort, move.SettingsPath)
	}

	// --update returns here, so anything an EXISTING project still needs
	// must be done before this point. The .env stub is one of them: the
	// fresh-init path creates it, but a project migrating an old
	// WORKFLOW.md never reaches that code, so `init --update` left the
	// operator with `api_key: $LINEAR_API_KEY` and no file to define it
	// in — the daemon then hard-failed startup with "missing
	// tracker.api_key" and no indication of where to put it.
	//
	// The tracker kind comes from the loaded config (WORKFLOW.md and the
	// settings files), not --tracker, which is not required for --update.
	updateDir := filepath.Join(filepath.Dir(workflowPath), ".itervox")
	ensureEnvStub(updateDir, trackerKindFromWorkflow(workflowPath))
	// The nested .itervox/.gitignore is self-healed on daemon startup,
	// but write it now so the .env just created is covered before the
	// operator's next `git add`.
	if err := finalizeItervoxGitignore(updateDir); err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox init --update: %v\n", err)
	}
	scaffoldPlaybooks(updateDir, "itervox init --update", out) // #218: only the missing ones
	return nil
}
