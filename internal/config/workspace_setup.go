package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// #250 — workspace setup: files copied into each new worktree
// (workspace.copy_files) and a per-workspace TCP port (workspace.port_range).

const (
	// DefaultCopyFilesMaxBytes caps how much workspace.copy_files copies
	// into one issue's worktree per run (10 MB).
	DefaultCopyFilesMaxBytes = 10 * 1024 * 1024
	// DefaultWorkspacePortRange is the range ITERVOX_PORT is picked from.
	DefaultWorkspacePortRange = "41000-41999"
)

// parseWorkspaceSetup reads workspace.copy_files, workspace.copy_files_max_bytes
// and workspace.port_range into cfg. A copy_files entry that is absolute or
// climbs out of the repository root, and a malformed port range, fail the
// load: both would otherwise do something the operator did not ask for.
func parseWorkspaceSetup(ws map[string]any, cfg *WorkspaceConfig) error {
	cfg.CopyFiles = strSliceField(ws, "copy_files", nil)
	for i, p := range cfg.CopyFiles {
		clean, err := validateCopyFilesEntry(p)
		if err != nil {
			return fmt.Errorf("config: workspace.copy_files[%d]: %w", i, err)
		}
		cfg.CopyFiles[i] = clean
	}
	cfg.CopyFilesMaxBytes = positiveIntField(ws, "copy_files_max_bytes", DefaultCopyFilesMaxBytes)
	cfg.PortRange = strField(ws, "port_range", DefaultWorkspacePortRange)
	lo, hi, err := parsePortRange(cfg.PortRange)
	if err != nil {
		return fmt.Errorf("config: workspace.port_range: %w", err)
	}
	cfg.PortMin, cfg.PortMax = lo, hi
	return nil
}

// validateCopyFilesEntry checks one workspace.copy_files entry (a path or a
// filepath.Match glob, relative to the repository root) and returns it
// cleaned, with forward slashes. It refuses an empty entry, an absolute
// path, and any entry with a ".." element, so no match can resolve outside
// the repository root.
func validateCopyFilesEntry(p string) (string, error) {
	s := strings.TrimSpace(p)
	if s == "" {
		return "", errors.New("empty path")
	}
	if filepath.IsAbs(s) || strings.HasPrefix(s, "/") || strings.HasPrefix(s, `\`) || filepath.VolumeName(s) != "" {
		return "", fmt.Errorf("%q is absolute; paths are relative to the repository root", p)
	}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", fmt.Errorf("%q leaves the repository root", p)
		}
	}
	clean := filepath.ToSlash(filepath.Clean(s))
	if clean == "." {
		return "", fmt.Errorf("%q names the repository root itself", p)
	}
	if _, err := filepath.Match(clean, ""); err != nil {
		return "", fmt.Errorf("%q is not a valid pattern: %w", p, err)
	}
	return clean, nil
}

// parsePortRange parses "LOW-HIGH" (inclusive, 1024..65535, LOW <= HIGH).
func parsePortRange(s string) (lo, hi int, err error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return 0, 0, fmt.Errorf("%q is not LOW-HIGH (for example %s)", s, DefaultWorkspacePortRange)
	}
	lo, errLo := strconv.Atoi(strings.TrimSpace(a))
	hi, errHi := strconv.Atoi(strings.TrimSpace(b))
	if errLo != nil || errHi != nil {
		return 0, 0, fmt.Errorf("%q is not LOW-HIGH (for example %s)", s, DefaultWorkspacePortRange)
	}
	if lo < 1024 || hi > 65535 || lo > hi {
		return 0, 0, fmt.Errorf("%q must be within 1024-65535 with LOW <= HIGH", s)
	}
	return lo, hi, nil
}
