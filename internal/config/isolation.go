package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Isolation modes (#258). A profile's agent runs either as today (none) or
// inside an operating-system sandbox that confines what it may write and
// read (process). The sandbox itself lives in internal/agent/isolation.go.
const (
	// IsolationNone is the default: the agent runs with the daemon user's
	// full filesystem access, exactly as before #258.
	IsolationNone = "none"
	// IsolationProcess runs the agent subprocess (and every child it
	// starts) inside bubblewrap or Landlock on Linux, or sandbox-exec on
	// macOS. When none of those works the run does not start.
	IsolationProcess = "process"
)

// ErrIsolationOnSSHHost is the load error for process isolation combined
// with SSH workers: the sandbox wraps a local subprocess, and an SSH turn's
// local subprocess is ssh itself, so the remote agent would run unconfined.
var ErrIsolationOnSSHHost = errors.New(`config: isolation "process" is not supported on SSH workers yet`)

// ValidateIsolationMode rejects an unknown isolation value. allowEmpty is
// true for a profile, where empty means "inherit agent.isolation".
//
// Unlike permission_mode, an unknown value is a hard error: the only
// fallback would be "none", which is less confined than what the operator
// asked for.
func ValidateIsolationMode(field, mode string, allowEmpty bool) error {
	switch mode {
	case IsolationNone, IsolationProcess:
		return nil
	case "":
		if allowEmpty {
			return nil
		}
	}
	return fmt.Errorf("config: %s must be %q or %q, got %q", field, IsolationNone, IsolationProcess, mode)
}

// ResolveIsolation returns the isolation a profile runs with: its own
// value, or agent.isolation when it sets none, or "none".
func ResolveIsolation(profileMode, defaultMode string) string {
	if m := strings.TrimSpace(profileMode); m != "" {
		return m
	}
	if m := strings.TrimSpace(defaultMode); m != "" {
		return m
	}
	return IsolationNone
}

// IsolatedProfiles returns the enabled profiles that run with process
// isolation, sorted; "(default)" stands for agent.command when
// agent.isolation is process.
func IsolatedProfiles(a AgentConfig) []string {
	var out []string
	for name, p := range a.Profiles {
		if ProfileEnabled(p) && ResolveIsolation(p.Isolation, a.Isolation) == IsolationProcess {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if a.Isolation == IsolationProcess {
		out = append([]string{"(default)"}, out...)
	}
	return out
}

// ValidateIsolationHosts refuses process isolation when agent.ssh_hosts is
// set (#258): with SSH hosts every dispatch goes to a remote host, where
// the sandbox cannot reach. Sandboxing SSH workers is out of scope for now.
func ValidateIsolationHosts(a AgentConfig) error {
	if len(a.SSHHosts) == 0 {
		return nil
	}
	if names := IsolatedProfiles(a); len(names) > 0 {
		return fmt.Errorf("%w: %s set isolation: process while agent.ssh_hosts is set; "+
			"remove agent.ssh_hosts or set isolation: none for them", ErrIsolationOnSSHHost, strings.Join(names, ", "))
	}
	return nil
}

// profileIsolation is the isolation part of one parsed profile.
type profileIsolation struct {
	Isolation           string
	IsolationExtraWrite []string
	IsolationExtraDeny  []string
}

// profileIsolationFields parses a profile's isolation keys: the mode and
// the two path lists, each entry absolute or starting with ~/.
func profileIsolationFields(m map[string]any, name string) (profileIsolation, error) {
	out := profileIsolation{Isolation: strings.TrimSpace(strField(m, "isolation", ""))}
	if err := ValidateIsolationMode("agent.profiles."+name+".isolation", out.Isolation, true); err != nil {
		return profileIsolation{}, err
	}
	var err error
	if out.IsolationExtraWrite, err = isolationPathsField(m, name, "isolation_extra_write"); err != nil {
		return profileIsolation{}, err
	}
	if out.IsolationExtraDeny, err = isolationPathsField(m, name, "isolation_extra_deny"); err != nil {
		return profileIsolation{}, err
	}
	return out, nil
}

// isolationPathsField reads one path list, expanding ~ and refusing
// relative paths: a sandbox rule must not depend on the daemon's working
// directory.
func isolationPathsField(m map[string]any, name, key string) ([]string, error) {
	raw := strSliceField(m, key, nil)
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		expanded := expandTilde(p)
		if !filepath.IsAbs(expanded) {
			return nil, fmt.Errorf("config: agent.profiles.%s.%s entry %q must be an absolute path or start with ~/", name, key, p)
		}
		out = append(out, filepath.Clean(expanded))
	}
	return out, nil
}
