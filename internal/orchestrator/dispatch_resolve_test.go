package orchestrator

import (
	"testing"

	"github.com/vnovick/itervox/internal/config"
)

// TestResolveBackendForIssue covers the five resolution layers and their
// interactions. Each row is one realistic configuration the orchestrator
// can see in practice.
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
			name:           "default backend overrides inference",
			defaultCmd:     "claude",
			defaultBackend: "codex",
			wantCmd:        "claude",
			wantRunnerCmd:  "@@itervox-backend=codex claude",
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
			name:          "profile.Backend overrides backend, keeps cmd",
			defaultCmd:    "claude",
			profile:       &config.AgentProfile{Backend: "codex"},
			wantCmd:       "claude",
			wantRunnerCmd: "@@itervox-backend=codex claude",
			wantBackend:   "codex",
		},
		{
			name:          "per-issue override wins over everything",
			defaultCmd:    "claude",
			profile:       &config.AgentProfile{Command: "codex", Backend: "codex"},
			issueOverride: "claude",
			wantCmd:       "codex", // profile.Command still applies for cmd
			wantRunnerCmd: "@@itervox-backend=claude codex",
			wantBackend:   "claude",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, runnerCmd, backend := resolveBackendForIssue(
				tc.defaultCmd, tc.defaultBackend, tc.profile, tc.issueOverride,
			)
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
