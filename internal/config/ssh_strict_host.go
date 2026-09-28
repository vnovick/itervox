package config

import (
	"fmt"
	"slices"
	"strings"
)

// sshStrictHostModes is the set of StrictHostKeyChecking values `man
// ssh_config` accepts. It is the single source of truth for both config
// validation (ValidateSSHStrictHostChecking) and the agent package's runtime
// setters, which import it via IsValidSSHStrictHostMode (CORE-140).
var sshStrictHostModes = []string{"accept-new", "ask", "no", "off", "yes"}

// IsValidSSHStrictHostMode reports whether mode is a StrictHostKeyChecking
// value ssh accepts. Matching is exact: ssh_config values are lowercase and
// "Yes" or "strict" are not synonyms.
func IsValidSSHStrictHostMode(mode string) bool {
	return slices.Contains(sshStrictHostModes, mode)
}

// ValidateSSHStrictHostChecking rejects an agent.ssh_strict_host_checking or
// agent.ssh_strict_host_by_host value outside the ssh_config set (CORE-140).
// An empty default means "unset" and keeps the agent package's accept-new
// default. Before this check an invalid value was dropped silently at
// startup, so an operator who meant to pin host keys ("strict", "Yes") ran
// with accept-new — unknown hosts trusted on first contact — and no signal.
func ValidateSSHStrictHostChecking(defaultMode string, byHost map[string]string) error {
	valid := strings.Join(sshStrictHostModes, ", ")
	if defaultMode != "" && !IsValidSSHStrictHostMode(defaultMode) {
		return fmt.Errorf("agent.ssh_strict_host_checking: invalid mode %q (must be one of: %s)", defaultMode, valid)
	}
	hosts := make([]string, 0, len(byHost))
	for host := range byHost {
		hosts = append(hosts, host)
	}
	slices.Sort(hosts)
	for _, host := range hosts {
		if mode := byHost[host]; !IsValidSSHStrictHostMode(mode) {
			return fmt.Errorf("agent.ssh_strict_host_by_host[%q]: invalid mode %q (must be one of: %s)", host, mode, valid)
		}
	}
	return nil
}

// sshStrictHostCheckingField reads agent.ssh_strict_host_checking. Unlike
// strField, which reads any non-string value as "unset", it rejects a
// non-string (fix round 1, M6): YAML `true` or `1` would otherwise silently
// keep the accept-new default. A missing or null key is unset (""); the mode
// itself is checked by ValidateSSHStrictHostChecking.
func sshStrictHostCheckingField(agent map[string]any) (string, error) {
	v, ok := agent["ssh_strict_host_checking"]
	if !ok || v == nil {
		return "", nil
	}
	s, isString := v.(string)
	if !isString {
		return "", fmt.Errorf("config: agent.ssh_strict_host_checking must be a string (one of: %s), got %T %v; quote the value in WORKFLOW.md",
			strings.Join(sshStrictHostModes, ", "), v, v)
	}
	return s, nil
}
