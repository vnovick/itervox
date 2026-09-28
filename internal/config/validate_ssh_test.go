package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

// TestValidateDispatchRejectsInvalidSSHStrictHostModes is CORE-140's config
// half: an ssh_strict_host_checking value (default or per-host) outside the
// ssh_config set must fail validation instead of being silently dropped at
// startup, which downgraded an intended strict pin to accept-new.
func TestValidateDispatchRejectsInvalidSSHStrictHostModes(t *testing.T) {
	cases := []struct {
		name    string
		extras  string
		wantErr string
	}{
		{"invalid_default", "agent:\n  ssh_strict_host_checking: \"strict\"\n", `agent.ssh_strict_host_checking: invalid mode "strict"`},
		{"invalid_per_host", "agent:\n  ssh_strict_host_by_host:\n    \"prod.example.com\": \"Yes\"\n", `agent.ssh_strict_host_by_host["prod.example.com"]: invalid mode "Yes"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimalV2(tc.extras)))
			require.NoError(t, err)
			err = config.ValidateDispatch(cfg)
			require.Error(t, err, "an invalid StrictHostKeyChecking mode must fail validation")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestValidateDispatchAcceptsValidSSHStrictHostModes is the control: every
// ssh_config mode, and an unset default, still validates.
func TestValidateDispatchAcceptsValidSSHStrictHostModes(t *testing.T) {
	for _, mode := range []string{"yes", "no", "ask", "accept-new", "off"} {
		t.Run(mode, func(t *testing.T) {
			extras := "agent:\n  ssh_strict_host_checking: \"" + mode + "\"\n  ssh_strict_host_by_host:\n    \"h.example.com\": \"" + mode + "\"\n"
			cfg, err := config.Load(workflowWithContent(t, minimalV2(extras)))
			require.NoError(t, err)
			assert.NoError(t, config.ValidateDispatch(cfg))
		})
	}
	cfg, err := config.Load(workflowWithContent(t, minimalV2("")))
	require.NoError(t, err)
	assert.NoError(t, config.ValidateDispatch(cfg), "an unset mode keeps the agent default and is valid")
}

// TestLoadRejectsNonStringSSHStrictHostChecking is fix round 1's M6: a
// non-string value (e.g. YAML `true`) used to be read as "unset" by
// strField, silently keeping accept-new.
func TestLoadRejectsNonStringSSHStrictHostChecking(t *testing.T) {
	for _, raw := range []string{"true", "1", "[yes]"} {
		t.Run(raw, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimalV2("agent:\n  ssh_strict_host_checking: "+raw+"\n")))
			if err == nil {
				err = config.ValidateDispatch(cfg)
			}
			require.Error(t, err, "a non-string ssh_strict_host_checking must be rejected, not treated as unset")
			assert.Contains(t, err.Error(), "agent.ssh_strict_host_checking")
			assert.Contains(t, err.Error(), "must be a string")
		})
	}
}

// TestLoadAcceptsUnquotedSSHStrictHostModes: yaml.v3 decodes unquoted
// yes/no/off into interface{} as strings, so M6's type check must not break
// the natural unquoted spelling.
func TestLoadAcceptsUnquotedSSHStrictHostModes(t *testing.T) {
	for _, mode := range []string{"yes", "no", "off", "accept-new"} {
		t.Run(mode, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimalV2("agent:\n  ssh_strict_host_checking: "+mode+"\n")))
			require.NoError(t, err)
			require.NoError(t, config.ValidateDispatch(cfg))
			assert.Equal(t, mode, cfg.Agent.SSHStrictHostChecking)
		})
	}
}
