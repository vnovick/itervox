package agent

import (
	"runtime"
	"slices"
	"testing"
)

// TestDirectInvocation pins how a local turn starts the CLI: a clean
// absolute path runs directly; anything else goes through the login shell
// when there is one; with no shell (a Windows host without bash,
// procgroup.Shell == "") the command is split and executed directly.
func TestDirectInvocation(t *testing.T) {
	abs := "/usr/local/bin/claude"
	if runtime.GOOS == "windows" {
		abs = `C:\tools\claude.exe`
	}
	cases := []struct {
		command, shell string
		name           string
		head           []string
		direct         bool
	}{
		{abs, "/bin/zsh", abs, nil, true},
		{"claude", "/bin/zsh", "", nil, false},
		{"claude --model opus", "bash", "", nil, false},
		{abs + " --model opus", "bash", "", nil, false},
		{"claude", "", "claude", nil, true},
		{"claude --model opus", "", "claude", []string{"--model", "opus"}, true},
		{"   ", "", "codex", nil, true},
	}
	for _, tc := range cases {
		name, head, direct := directInvocation(tc.command, tc.shell, "codex")
		if name != tc.name || !slices.Equal(head, tc.head) || direct != tc.direct {
			t.Errorf("directInvocation(%q, %q) = %q %v %v; want %q %v %v",
				tc.command, tc.shell, name, head, direct, tc.name, tc.head, tc.direct)
		}
	}
}
