package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The shell command strings the runners build for a compound command: the
// flags (and the prompt redirect) go inside the group, after the final
// command, for the local login shell and the SSH worker's remote bash
// alike. A command validation refuses (a pipe) reaching a runner anyway
// keeps the old form, the flags after the whole command line.
func TestSpliceTurnArgsShellCommandStrings(t *testing.T) {
	assert.Equal(t,
		"{ source ~/.nvm/nvm.sh; claude"+sharedFlagsStrFor(PermissionBypass)+" -p"+remotePromptRedirect+"; }",
		buildShellCmd("{ source ~/.nvm/nvm.sh; claude; }", nil, "hi", remotePromptRedirect, PermissionBypass),
		"SSH: the remote bash runs the group with the flags inside it")
	assert.Equal(t,
		"(cd x && codex -C '/ws' exec --json --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check -"+localPromptRedirect+")",
		buildCodexShellCmd("(cd x && codex)", nil, "/ws", localPromptRedirect, PermissionBypass))
	turn := cliTurn{name: "opencode", fallback: "opencode", command: "source f && opencode --model m", subcommand: "run", args: []string{"--format", "json"}}
	assert.Equal(t, "source f && opencode run --model m --format json"+localPromptRedirect, turn.shellCmd(turn.localPromptTail()))

	// Refused at load; a runner that gets it anyway appends as before.
	assert.Equal(t, "claude | tee log --x", spliceTurnArgs("claude | tee log", "", " --x"))
	assert.Equal(t, "opencode run | tee log --x", spliceTurnArgs("opencode | tee log", "run", " --x"))
}
