package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// setItervoxBinEnv exports ITERVOX_BIN early in process startup so every child
// process — workers, hooks, debug shells launched from `itervox` subcommands —
// inherits an unambiguous path to the running binary. Without this, hooks
// inherit the daemon's PATH and resolve `itervox` to whatever is on PATH
// (typically the older system-installed binary), which is the canonical
// failure mode described in P0-F. Idempotent: if the operator pinned
// ITERVOX_BIN externally, the explicit value wins.
func setItervoxBinEnv() {
	if existing := os.Getenv("ITERVOX_BIN"); existing != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		slog.Warn("itervox: cannot resolve own binary path; ITERVOX_BIN will be unset",
			"err", err.Error(),
			"hint", "set ITERVOX_BIN manually so hooks can find the running daemon")
		return
	}
	if err := os.Setenv("ITERVOX_BIN", exe); err != nil {
		slog.Warn("itervox: cannot set ITERVOX_BIN env var",
			"err", err.Error())
	}
}

// refreshItervoxBinSymlink writes .itervox/bin/itervox → os.Executable() so
// hooks, debug shells, and operator-sourced shell aliases have a stable
// project-local handle that points to the daemon that last booted in this
// directory. Idempotent: any existing symlink (correct or stale) is removed
// before the new one is written. P0-H.
func refreshItervoxBinSymlink(projectRoot string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own binary: %w", err)
	}
	binDir := filepath.Join(projectRoot, ".itervox", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", binDir, err)
	}
	target := filepath.Join(binDir, "itervox")
	// os.Remove ignores ErrNotExist-style failures and we want to surface real
	// errors (permission denied, etc.) before the Symlink attempt.
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", target, err)
	}
	if err := os.Symlink(exe, target); err != nil {
		return fmt.Errorf("symlink %s -> %s: %w", target, exe, err)
	}
	return nil
}
