//go:build unix

package agent

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// loginShell — test fallback
// ---------------------------------------------------------------------------

func TestLoginShellFallback(t *testing.T) {
	orig := os.Getenv("SHELL")
	defer func() { _ = os.Setenv("SHELL", orig) }()
	_ = os.Unsetenv("SHELL")
	assert.Equal(t, "bash", loginShell())
}

func TestLoginShellFromEnv(t *testing.T) {
	orig := os.Getenv("SHELL")
	defer func() { _ = os.Setenv("SHELL", orig) }()
	_ = os.Setenv("SHELL", "/bin/zsh")
	assert.Equal(t, "/bin/zsh", loginShell())
}

func TestBuildValidationShellCommand_DetachesFromTTY(t *testing.T) {
	cmd := buildValidationShellCommand(context.Background(), "/bin/zsh", "claude --version")
	require.Equal(t, []string{"/bin/zsh", "-ilc", "claude --version"}, cmd.Args)
	require.NotNil(t, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.Setsid)
}
