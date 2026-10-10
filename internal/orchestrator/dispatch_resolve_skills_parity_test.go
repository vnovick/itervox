package orchestrator

import (
	"testing"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/skills"
)

// TestSkillsProfileBackendMatchesDispatchResolver (#86): the skills
// reference validator picks the inventory to check a profile's prompt
// against with skills.ProfileBackend. It must name the runner that dispatch
// actually uses — resolveDispatchTarget's runner command, routed the way
// agent.MultiRunner routes it (codex → codex, anything else → the default
// Claude runner) — for every combination of default command/backend and
// profile command/backend, including padded, mixed-case, unknown and hinted
// values.
func TestSkillsProfileBackendMatchesDispatchResolver(t *testing.T) {
	commands := []string{
		"", "claude", "codex", "claude --model opus", "codex exec",
		"./wrap", "/opt/bin/codex-wrapper.sh", "env FOO=1 codex",
		"OPENAI_API_KEY=x codex exec", "CODEX_HOME=/x claude",
		config.BackendHintPrefix + "codex ./wrap", config.BackendHintPrefix + "claude codex",
		config.BackendHintPrefix + "gemini ./wrap", " codex", "codex ", "Codex",
	}
	backends := []string{"", "claude", "codex", " codex", "codex ", "Codex", "gemini"}
	checked := 0
	for _, dc := range commands {
		for _, db := range backends {
			for _, pc := range commands {
				for _, pb := range backends {
					profile := config.AgentProfile{Command: pc, Backend: pb}
					target := resolveDispatchTarget(dispatchTargetInput{
						DefaultCommand: dc, DefaultBackend: db, Profile: &profile,
					})
					want := "claude"
					if agent.BackendFromCommand(target.RunnerCommand) == "codex" {
						want = "codex"
					}
					got := skills.ProfileBackend(profile, skills.RefBackendDefaults{Command: dc, Backend: db})
					if got != want {
						t.Fatalf("default (%q, %q) profile (%q, %q): skills says %q, dispatch runs %q (runner command %q)",
							dc, db, pc, pb, got, want, target.RunnerCommand)
					}
					checked++
				}
			}
		}
	}
	t.Logf("checked %d combinations", checked)
}
