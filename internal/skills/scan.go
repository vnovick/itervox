package skills

import (
	"errors"
	"time"
)

// ScanOptions tweaks the scanner's behavior. All fields are optional; the
// zero value scans everything reachable from projectDir + homeDir.
type ScanOptions struct {
	// SkipUserHome disables the ~/. walk (test isolation).
	SkipUserHome bool
	// SkipCodex disables the best-effort Codex scanner.
	SkipCodex bool
	// SkipPlugins disables the plugin scanner.
	SkipPlugins bool
}

// Scan walks the project + user-home Claude/Codex layout and returns a
// populated Inventory. Stitches the per-component scanners (T-78..T-83)
// together; analytics (T-100) and runtime evidence (T-98..T-99) are populated
// later by the orchestrator.
//
// Contract:
//   - projectDir is the WORKFLOW.md root.
//   - homeDir is the user home; empty string disables user-home scan.
//   - Returns a non-nil Inventory on success.
//   - Aggregates all errors via errors.Join; the inventory is partial in that case.
//   - Bounded latency: Phase-1 scan should stay under ~200ms on a developer laptop.
func Scan(projectDir, homeDir string, opts ScanOptions) (*Inventory, error) {
	inv := &Inventory{ScanTime: time.Now()}

	effectiveHome := homeDir
	if opts.SkipUserHome {
		effectiveHome = ""
	}

	var errs []error

	// Skills.
	claudeSkills, err := scanClaudeSkills(projectDir, effectiveHome)
	if err != nil {
		errs = append(errs, err)
	} else {
		inv.Skills = append(inv.Skills, claudeSkills...)
	}

	// Plugins.
	if !opts.SkipPlugins {
		plugins, err := scanClaudePlugins(projectDir, effectiveHome)
		if err != nil {
			errs = append(errs, err)
		} else {
			inv.Plugins = append(inv.Plugins, plugins...)
		}
	}

	// MCP servers.
	mcpServers, err := scanMCPServers(projectDir, effectiveHome)
	if err != nil {
		errs = append(errs, err)
	} else {
		inv.MCPServers = mcpServers
	}

	// Hooks.
	hooks, err := scanHooks(projectDir, effectiveHome)
	if err != nil {
		errs = append(errs, err)
	} else {
		inv.Hooks = hooks
	}

	// Instructions.
	instructions, err := scanInstructions(projectDir, effectiveHome)
	if err != nil {
		errs = append(errs, err)
	} else {
		inv.Instructions = instructions
	}

	// Codex (best-effort).
	if !opts.SkipCodex {
		codexSkills, codexPlugins, codexHooks, codexInstructions, err := scanCodex(projectDir, effectiveHome)
		if err != nil {
			errs = append(errs, err)
		} else {
			inv.Skills = append(inv.Skills, codexSkills...)
			inv.Plugins = append(inv.Plugins, codexPlugins...)
			inv.Hooks = append(inv.Hooks, codexHooks...)
			inv.Instructions = append(inv.Instructions, codexInstructions...)
		}
	}

	if len(errs) > 0 {
		return inv, errors.Join(errs...)
	}
	return inv, nil
}
