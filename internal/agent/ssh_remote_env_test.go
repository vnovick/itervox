package agent_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
)

// 0.3.4 (guardrails on SSH hosts): a turn's RemoteEnv reaches the remote
// script — exported after the login profile ran, PATH with the shim
// directory first — for every runner's SSH path, while its secret value
// (the guardrail token) is in no ssh argument: the script travels on ssh's
// stdin. Each turn reverse-forwards a fresh socket under ForwardDir and
// exports its path; the forward fails the connection when refused.
func TestRemoteRunEnvTravelsOnStdinNotArgv(t *testing.T) {
	const secret = "grd-secret-value-4b1e"
	runners := map[string]agent.Runner{
		"claude": agent.NewClaudeRunner(), "codex": agent.NewCodexRunner(), "opencode": agent.NewOpenCodeRunner(),
	}
	for name, runner := range runners {
		t.Run(name, func(t *testing.T) {
			payloadLog := installRecordingFakeSSH(t)
			argvLog := filepath.Join(t.TempDir(), "argv.log")
			t.Setenv("FAKE_SSH_ARGV_LOG", argvLog)
			ctx := agent.WithRunEnv(context.Background(), agent.RunEnv{Remote: &agent.RemoteEnv{
				Exports:     []string{"GIT_CONFIG_PARAMETERS='core.hooksPath'='/r/hooks'", "ITERVOX_GUARDRAIL_TOKEN=" + secret, "bad name=x"},
				PathPrepend: "/r/bin",
				ForwardDir:  "/r", ForwardTarget: "127.0.0.1:8090", ForwardEnv: "ITERVOX_GUARDRAIL_SOCKET",
			}})
			for range 2 {
				_, _ = runner.RunTurn(ctx, slog.Default(), nil, nil, "hi", "/ws", name, "worker.example.test", "", 30000, 60000, agent.PermissionBypass)
			}
			payload, err := os.ReadFile(payloadLog)
			require.NoError(t, err)
			argv, err := os.ReadFile(argvLog)
			require.NoError(t, err)
			p := string(payload)
			assert.Contains(t, p, "export GIT_CONFIG_PARAMETERS=''\\''core.hooksPath'\\''='\\''/r/hooks'\\'''; ")
			assert.Contains(t, p, "export ITERVOX_GUARDRAIL_TOKEN='"+secret+"'; ")
			assert.Contains(t, p, `export PATH='/r/bin':"$PATH"; `)
			assert.NotContains(t, p, "bad name")
			assert.Less(t, strings.Index(p, "export PATH="), strings.Index(p, "itervox_prompt="), "set before the agent command")
			assert.False(t, strings.Contains(string(argv), secret), "the token is never an ssh argument")

			var socks []string
			for _, a := range strings.Split(string(argv), "\n") {
				if strings.HasPrefix(a, "/r/cb-") {
					sock, target, _ := strings.Cut(a, ":")
					assert.Equal(t, "127.0.0.1:8090", target)
					socks = append(socks, sock)
					assert.Contains(t, p, "export ITERVOX_GUARDRAIL_SOCKET='"+sock+"'; ")
				}
			}
			require.Len(t, socks, 2, "one reverse forward per turn: %s", argv)
			assert.NotEqual(t, socks[0], socks[1], "a fresh socket per turn")
			assert.Equal(t, 2, strings.Count(string(argv), "ExitOnForwardFailure=yes"))
		})
	}
}
