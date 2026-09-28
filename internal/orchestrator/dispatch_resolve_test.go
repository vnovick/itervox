package orchestrator

import (
	"testing"

	"github.com/vnovick/itervox/internal/config"
)

// TestResolveBackendForIssue covers the resolution layers of
// resolveDispatchTarget and their interactions. Each row is one realistic
// configuration the orchestrator can see in practice. CORE-115: the three
// rows that used to pair a claude/codex command with the other backend's
// hint now keep the command's own backend (the pair was unrunnable).
func TestResolveBackendForIssue(t *testing.T) {
	tests := []struct {
		name           string
		defaultCmd     string
		defaultBackend string
		profile        *config.AgentProfile
		issueOverride  string
		wantCmd        string
		wantRunnerCmd  string
		wantBackend    string
	}{
		{
			name:           "all defaults — backend inferred from command",
			defaultCmd:     "claude",
			defaultBackend: "",
			wantCmd:        "claude",
			wantRunnerCmd:  "claude",
			wantBackend:    "claude",
		},
		{
			name:           "default backend mismatching the command binary is refused",
			defaultCmd:     "claude",
			defaultBackend: "codex",
			wantCmd:        "claude",
			wantRunnerCmd:  "claude",
			wantBackend:    "claude",
		},
		{
			name:           "default backend overrides inference for a wrapper",
			defaultCmd:     "/opt/bin/agent",
			defaultBackend: "codex",
			wantCmd:        "/opt/bin/agent",
			wantRunnerCmd:  "@@itervox-backend=codex /opt/bin/agent",
			wantBackend:    "codex",
		},
		{
			name:       "profile.Command replaces cmd and backend",
			defaultCmd: "claude",
			profile:    &config.AgentProfile{Command: "codex"},
			wantCmd:    "codex",
			// runnerCmd should equal the profile command; backend inferred from it.
			wantRunnerCmd: "codex",
			wantBackend:   "codex",
		},
		{
			name:          "profile.Backend overrides backend, keeps cmd (wrapper)",
			defaultCmd:    "my-agent",
			profile:       &config.AgentProfile{Backend: "codex"},
			wantCmd:       "my-agent",
			wantRunnerCmd: "@@itervox-backend=codex my-agent",
			wantBackend:   "codex",
		},
		{
			name:          "profile.Backend mismatching the command binary is refused",
			defaultCmd:    "claude",
			profile:       &config.AgentProfile{Backend: "codex"},
			wantCmd:       "claude",
			wantRunnerCmd: "claude",
			wantBackend:   "claude",
		},
		{
			name:          "per-issue override wins over everything (wrapper)",
			defaultCmd:    "claude",
			profile:       &config.AgentProfile{Command: "run-agent.sh", Backend: "codex"},
			issueOverride: "claude",
			wantCmd:       "run-agent.sh", // profile.Command still applies for cmd
			wantRunnerCmd: "@@itervox-backend=claude run-agent.sh",
			wantBackend:   "claude",
		},
		{
			name:          "per-issue override mismatching the command binary is refused",
			defaultCmd:    "claude",
			profile:       &config.AgentProfile{Command: "codex", Backend: "codex"},
			issueOverride: "claude",
			wantCmd:       "codex",
			wantRunnerCmd: "codex",
			wantBackend:   "codex",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveDispatchTarget(dispatchTargetInput{
				DefaultCommand: tc.defaultCmd, DefaultBackend: tc.defaultBackend,
				Profile: tc.profile, IssueBackend: tc.issueOverride,
			})
			cmd, runnerCmd, backend := got.Command, got.RunnerCommand, got.Backend
			if cmd != tc.wantCmd {
				t.Errorf("cmd: got %q, want %q", cmd, tc.wantCmd)
			}
			if runnerCmd != tc.wantRunnerCmd {
				t.Errorf("runnerCmd: got %q, want %q", runnerCmd, tc.wantRunnerCmd)
			}
			if backend != tc.wantBackend {
				t.Errorf("backend: got %q, want %q", backend, tc.wantBackend)
			}
		})
	}
}
