package config_test

import (
	"os"
	"testing"
)

// TestMain isolates the package from the server bind env overrides
// (CORE-058): a developer shell that exports PORT (common for web projects)
// must not change what config.Load returns in tests that do not ask for it.
func TestMain(m *testing.M) {
	for _, k := range []string{"ITERVOX_SERVER_HOST", "ITERVOX_SERVER_PORT", "PORT"} {
		_ = os.Unsetenv(k)
	}
	os.Exit(m.Run())
}
