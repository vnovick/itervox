package main

import (
	"os"
	"path/filepath"
	"strings"
)

// unsafeWorkspaceRoot reports a non-empty reason when `root` resolves to a
// path we should never recursively delete from. Returns "" when the path is
// safe to clear under. T-43 (gaps_280426 05.G-12).
//
// Refused paths:
//   - empty / "/" / "." (filesystem root or unset)
//   - the user's home directory (or any direct ancestor of the home dir)
//   - common system roots: /tmp, /var, /etc, /usr, /opt, /bin, /sbin, /lib,
//     /private (macOS), /System (macOS), /Library (macOS)
//
// The check is intentionally conservative — false-refusal is better than
// false-accept. If a user has a legitimate reason to clear from a sibling of
// /tmp they can configure `workspace.root: /tmp/itervox-workspaces/<name>`
// which contains a project-specific suffix and passes the guard.
func unsafeWorkspaceRoot(root string) string {
	cleaned := strings.TrimSpace(root)
	if cleaned == "" {
		return "empty path"
	}

	// Resolve to absolute form so a relative `./.` doesn't slip through.
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		// If we can't even resolve it, refuse — better safe than sorry.
		return "could not resolve to absolute path"
	}
	abs = filepath.Clean(abs)

	if abs == "/" || abs == "." {
		return "filesystem root"
	}

	// Home-directory check. UserHomeDir failures are non-fatal; absence of a
	// resolvable home just means we skip this guard step.
	if home, herr := os.UserHomeDir(); herr == nil {
		homeAbs := filepath.Clean(home)
		if abs == homeAbs {
			return "user home directory"
		}
		// Reject the parent dir of $HOME too (e.g. /Users on macOS).
		if homeAbs != "/" && abs == filepath.Dir(homeAbs) {
			return "parent of user home directory"
		}
	}

	systemRoots := []string{
		"/tmp", "/var", "/etc", "/usr", "/opt",
		"/bin", "/sbin", "/lib",
		"/private", "/System", "/Library", "/Volumes",
		// Common parents of user home directories, listed statically so the
		// refusal does not depend on the platform the process runs on: the
		// $HOME ancestor check above only covers the *current* user's home,
		// so /Users must be refused even on Linux (and /home even on macOS).
		"/Users", "/home", "/root",
	}
	for _, sys := range systemRoots {
		if abs == sys {
			return "system directory " + sys
		}
	}
	return ""
}
