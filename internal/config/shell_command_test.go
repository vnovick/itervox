package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpliceAgentArgsPlacesFlagsAfterTheFinalCommand(t *testing.T) {
	cases := []struct{ name, command, sub, want string }{
		{"simple", "claude --model x", "", "claude --model x --F"},
		{"simple trailing space", "claude  ", "", "claude --F"},
		{"brace group", "{ source ~/.nvm/nvm.sh; claude; }", "", "{ source ~/.nvm/nvm.sh; claude --F; }"},
		{"brace group with args", "{ a; claude --model 'm x'; }", "", "{ a; claude --model 'm x' --F; }"},
		{"subshell", "(cd x && claude)", "", "(cd x && claude --F)"},
		{"and list", "source f && claude", "", "source f && claude --F"},
		{"trailing semicolon", "claude;", "", "claude --F;"},
		{"newline list", "source f\nclaude\n", "", "source f\nclaude --F"},
		{"comment", "claude # pinned", "", "claude --F # pinned"},
		{"redirect stays", "claude 2>/dev/null", "", "claude --F 2>/dev/null"},
		{"env prefix", "{ X=1 env -u Y claude; }", "", "{ X=1 env -u Y claude --F; }"},
		{"exec", "source f && exec claude", "", "source f && exec claude --F"},
		{"itervox export prefix", "export A='v' || exit 1; claude", "", "export A='v' || exit 1; claude --F"},
		{"command substitution", `{ export P="$(nvm which 20)"; codex; }`, "", `{ export P="$(nvm which 20)"; codex --F; }`},
		{"subcommand simple", "opencode --model m", "run", "opencode run --model m --F"},
		{"subcommand group", "{ source f; opencode --model m; }", "run", "{ source f; opencode run --model m --F; }"},
		{"subcommand env", "A=1 opencode", "run", "A=1 opencode run --F"},
		{"wrapper script last", "cd sub && ./agent.sh", "", "cd sub && ./agent.sh --F"},
		{"background setup", "ollama serve & claude", "", "ollama serve & claude --F"},
		{"pipe before", "nvm ls | head -1; claude", "", "nvm ls | head -1; claude --F"},
		{"function", `run() { claude "$@"; }; run`, "", `run() { claude "$@"; }; run --F`},
		{"function subshell body", `run() ( claude "$@" ); run`, "", `run() ( claude "$@" ); run --F`},
		{"nested groups", "{ a; ( cd x; claude ); }", "", "{ a; ( cd x; claude --F ); }"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SpliceAgentArgs(tc.command, tc.sub, " --F")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseAgentCommandRefusesAmbiguousShapes(t *testing.T) {
	cases := []struct{ name, command, reason string }{
		{"pipe", "claude | tee log", "pipeline"},
		{"group piped", "{ a; claude; } | tee log", "pipeline"},
		{"background", "claude &", "background"},
		{"group background", "{ claude; } &", "background"},
		{"or", "setup || claude", "fails"},
		{"or group", "setup || { a; claude; }", "fails"},
		{"agent not last", "claude; echo done", "not its last command"},
		{"agent not last group", "{ claude; } && echo done", "not its last command"},
		{"if", "if true; then claude; fi", "compound command"},
		{"for", "for i in 1; do claude; done", "compound command"},
		{"paren in command", "claude (x)", "( inside a command"},
		{"heredoc", "claude <<EOF\nhi\nEOF", "here-document"},
		{"brace without semicolon", "{ claude }", "not closed"},
		{"unterminated quote", "claude 'x", "unterminated"},
		{"unclosed subshell", "(cd x && claude", "not closed"},
		{"dangling and", "claude &&", "ends in"},
		{"no program", "X=1", "no program"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAgentCommand(tc.command)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.reason)
			assert.Contains(t, err.Error(), `exec claude "$@"`, "the refusal names the wrapper-script form")
		})
	}
}

func TestFirstCommandTokenReadsTheFinalCommandOfACompoundCommand(t *testing.T) {
	assert.Equal(t, "claude", FirstCommandToken("{ source ~/.nvm/nvm.sh; claude; }"))
	assert.Equal(t, "codex", FirstCommandToken("(cd x && codex --model m)"))
	assert.Equal(t, "claude", FirstCommandToken("source f && claude"))
	assert.Equal(t, BackendClaude, BackendFromCommand("{ source ~/.nvm/nvm.sh; claude; }"))
	// Simple commands keep the original rule.
	assert.Equal(t, "claude", FirstCommandToken("ANTHROPIC_BASE_URL=x claude --model m"))
	assert.Equal(t, "exec", FirstCommandToken("exec claude"))
}

func TestValidateDispatchRefusesAnAmbiguousAgentCommand(t *testing.T) {
	cfg := validDispatchConfig(t)
	cfg.Agent.Profiles = map[string]AgentProfile{"fast": {Command: "claude | tee log"}}
	err := ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid profile "fast"`)
	assert.Contains(t, err.Error(), "pipeline")

	cfg.Agent.Profiles = map[string]AgentProfile{"nvm": {Command: "{ source ~/.nvm/nvm.sh; claude; }"}}
	require.NoError(t, ValidateDispatch(cfg), "a brace group ending in the CLI loads")

	cfg.Agent.Command = "claude; echo done"
	err = ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent.command")
}

func validDispatchConfig(t *testing.T) *Config {
	t.Helper()
	cfg := &Config{}
	cfg.SchemaVersion = LatestWorkflowSchemaVersion
	cfg.Tracker.Kind = "linear"
	cfg.Tracker.APIKey = "key"
	cfg.Tracker.ProjectSlug = "proj"
	cfg.Agent.Command = "claude"
	require.NoError(t, ValidateDispatch(cfg))
	return cfg
}
