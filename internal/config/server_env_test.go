package config_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// CORE-058 — server.host / server.port env overrides.
//
// host: ITERVOX_SERVER_HOST > server.host > 127.0.0.1
// port: ITERVOX_SERVER_PORT > PORT > server.port > 8090
//
// A PRESENT-but-invalid value is a hard config error naming the variable
// (os.LookupEnv, so an empty value is present, not unset).

// clearServerEnv unsets every variable the resolver reads, restoring them
// after the test, so a developer shell with PORT exported cannot leak in.
func clearServerEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ITERVOX_SERVER_HOST", "ITERVOX_SERVER_PORT", "PORT"} {
		t.Setenv(k, "") // registers restore
		require.NoError(t, os.Unsetenv(k))
	}
}

func TestServerHostPortEnvOverride(t *testing.T) {
	yaml := minimal("server:\n  host: 127.0.0.1\n  port: 9321\n")

	t.Run("no env: WORKFLOW.md wins", func(t *testing.T) {
		clearServerEnv(t)
		cfg, err := config.Load(workflowWithContent(t, yaml))
		require.NoError(t, err)
		assert.Equal(t, "127.0.0.1", cfg.Server.Host)
		assert.Equal(t, 9321, *cfg.Server.Port)
		assert.Equal(t, "server.host", cfg.Server.HostSource)
		assert.Equal(t, "server.port", cfg.Server.PortSource)
	})
	t.Run("ITERVOX_SERVER_HOST/PORT override WORKFLOW.md", func(t *testing.T) {
		clearServerEnv(t)
		t.Setenv("ITERVOX_SERVER_HOST", "0.0.0.0")
		t.Setenv("ITERVOX_SERVER_PORT", "18090")
		cfg, err := config.Load(workflowWithContent(t, yaml))
		require.NoError(t, err)
		assert.Equal(t, "0.0.0.0", cfg.Server.Host)
		assert.Equal(t, 18090, *cfg.Server.Port)
		assert.Equal(t, "ITERVOX_SERVER_HOST", cfg.Server.HostSource)
		assert.Equal(t, "ITERVOX_SERVER_PORT", cfg.Server.PortSource)
	})
	t.Run("defaults when neither env nor YAML", func(t *testing.T) {
		clearServerEnv(t)
		cfg, err := config.Load(workflowWithContent(t, minimal("")))
		require.NoError(t, err)
		assert.Equal(t, "127.0.0.1", cfg.Server.Host)
		assert.Equal(t, config.DefaultServerPort, *cfg.Server.Port)
		assert.Equal(t, "default", cfg.Server.HostSource)
		assert.Equal(t, "default", cfg.Server.PortSource)
	})
	t.Run("env 0 means OS picks; YAML 0 kept without env", func(t *testing.T) {
		clearServerEnv(t)
		t.Setenv("ITERVOX_SERVER_PORT", "0")
		cfg, err := config.Load(workflowWithContent(t, yaml))
		require.NoError(t, err)
		assert.Zero(t, *cfg.Server.Port)

		clearServerEnv(t)
		cfg, err = config.Load(workflowWithContent(t, minimal("server:\n  port: 0\n")))
		require.NoError(t, err)
		assert.Zero(t, *cfg.Server.Port, "an explicit YAML 0 still means OS picks")
	})
	t.Run("IPv6 and name-valued hosts", func(t *testing.T) {
		clearServerEnv(t)
		t.Setenv("ITERVOX_SERVER_HOST", "::")
		cfg, err := config.Load(workflowWithContent(t, yaml))
		require.NoError(t, err)
		assert.Equal(t, "::", cfg.Server.Host)

		t.Setenv("ITERVOX_SERVER_HOST", " itervox.internal ")
		cfg, err = config.Load(workflowWithContent(t, yaml))
		require.NoError(t, err)
		assert.Equal(t, "itervox.internal", cfg.Server.Host)
	})
}

func TestServerPortGenericPORTBelowItervoxSpecific(t *testing.T) {
	yaml := minimal("server:\n  port: 9321\n")

	clearServerEnv(t)
	t.Setenv("PORT", "8080")
	cfg, err := config.Load(workflowWithContent(t, yaml))
	require.NoError(t, err)
	assert.Equal(t, 8080, *cfg.Server.Port, "PORT beats server.port")
	assert.Equal(t, "PORT", cfg.Server.PortSource)

	t.Setenv("ITERVOX_SERVER_PORT", "18090")
	cfg, err = config.Load(workflowWithContent(t, yaml))
	require.NoError(t, err)
	assert.Equal(t, 18090, *cfg.Server.Port, "ITERVOX_SERVER_PORT beats PORT")
	assert.Equal(t, "ITERVOX_SERVER_PORT", cfg.Server.PortSource)
}

func TestServerPortEnvInvalidIsError(t *testing.T) {
	yaml := minimal("server:\n  port: 9321\n")
	for _, tc := range []struct{ key, val string }{
		{"ITERVOX_SERVER_PORT", ""},
		{"ITERVOX_SERVER_PORT", "abc"},
		{"ITERVOX_SERVER_PORT", "-1"},
		{"ITERVOX_SERVER_PORT", "65536"},
		{"ITERVOX_SERVER_PORT", "80.5"},
		{"PORT", ""},
		{"PORT", "http"},
		{"PORT", "70000"},
		{"ITERVOX_SERVER_HOST", ""},
		{"ITERVOX_SERVER_HOST", "http://example.com"},
		{"ITERVOX_SERVER_HOST", "example.com:8080"},
		{"ITERVOX_SERVER_HOST", "two words"},
		{"ITERVOX_SERVER_HOST", "a/b"},
	} {
		t.Run(tc.key+"="+tc.val, func(t *testing.T) {
			clearServerEnv(t)
			t.Setenv(tc.key, tc.val)
			_, err := config.Load(workflowWithContent(t, yaml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.key, "the error names the variable")
		})
	}
}

// M4-close BH LOW: only the winning port source is validated. A platform
// that injects a junk $PORT must not break a daemon whose operator pinned
// ITERVOX_SERVER_PORT; a junk $PORT on its own is still a hard error.
func TestServerEnvOnlyWinningPortSourceIsValidated(t *testing.T) {
	yaml := minimal("")
	t.Run("ITERVOX_SERVER_PORT wins over an invalid PORT", func(t *testing.T) {
		clearServerEnv(t)
		t.Setenv("PORT", "abc")
		t.Setenv("ITERVOX_SERVER_PORT", "9100")
		cfg, err := config.Load(workflowWithContent(t, yaml))
		require.NoError(t, err)
		assert.Equal(t, 9100, *cfg.Server.Port)
		assert.Equal(t, "ITERVOX_SERVER_PORT", cfg.Server.PortSource)
	})
	t.Run("an invalid PORT alone is still fatal", func(t *testing.T) {
		clearServerEnv(t)
		t.Setenv("PORT", "abc")
		_, err := config.Load(workflowWithContent(t, yaml))
		require.Error(t, err)
		assert.Contains(t, err.Error(), `PORT="abc"`)
	})
	t.Run("an invalid ITERVOX_SERVER_PORT is fatal even with a valid PORT", func(t *testing.T) {
		clearServerEnv(t)
		t.Setenv("PORT", "9200")
		t.Setenv("ITERVOX_SERVER_PORT", "")
		_, err := config.Load(workflowWithContent(t, yaml))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ITERVOX_SERVER_PORT")
	})
}
