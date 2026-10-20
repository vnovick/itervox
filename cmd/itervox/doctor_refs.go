package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/skills"
)

// Profile prompt reference check for `itervox doctor` (#86): scans the same
// skills / subagents inventory the dashboard shows and reports every
// profile prompt that names a skill or subagent that does not exist for the
// profile's backend. A missing reference does not stop dispatch, so it is a
// WARNING and never changes doctor's exit code.

// doctorHomeDir is the home directory doctor scans; a variable for tests.
var doctorHomeDir = func() string {
	home, _ := os.UserHomeDir()
	return home
}

func checkProfileRefs(cfg *config.Config, workflowPath string) []skills.InventoryIssue {
	if cfg == nil || len(cfg.Agent.Profiles) == 0 {
		return nil
	}
	projectDir := filepath.Dir(workflowPath)
	if abs, err := filepath.Abs(projectDir); err == nil {
		projectDir = abs
	}
	inv, _ := skills.Scan(projectDir, doctorHomeDir(), skills.ScanOptions{})
	defaults := skills.RefBackendDefaults{Command: cfg.Agent.Command, Backend: cfg.Agent.Backend}
	return skills.ValidateProfileRefs(inv, cfg.Agent.Profiles, defaults, cfg.Agent.SSHHosts)
}

func renderProfileRefIssues(b *strings.Builder, issues []skills.InventoryIssue) {
	for _, i := range issues {
		label := "WARNING"
		if i.Severity == "info" {
			label = "info"
		}
		fmt.Fprintf(b, "%s: %s — %s\n", label, i.Title, i.Description)
	}
}
