package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsDaemonCommandLine pins which command lines the process scan counts
// as a daemon.
func TestIsDaemonCommandLine(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{[]string{"itervox"}, true},
		{[]string{"/usr/local/bin/itervox", "--workflow", "WORKFLOW.md"}, true},
		{[]string{"/opt/homebrew/bin/itervox", "-verbose"}, true},
		{[]string{"/Applications/Itervox.app/Contents/MacOS/itervox-darwin-arm64", "--headless"}, true},
		{[]string{"itervox", "demo"}, true},
		{[]string{"/usr/local/bin/itervox", "guardrail-shim", "/p/.itervox/run/g/policy.json", "git", "push"}, false},
		{[]string{"/usr/local/bin/itervox", "guardrail-hook", "/p/policy.json", "pre-push"}, false},
		{[]string{"tmux", "new-session", "-d", "-s", "itervox-0a1b", "-c", "/p", "/bin/sh", "-c", "claude"}, false},
		{[]string{"/bin/sh", "-c", "PATH=/p/.itervox/run/guardrails/bin:$PATH claude"}, false},
		{[]string{"node", "/usr/lib/claude", "--settings", "/p/.itervox/x.json"}, false},
		{[]string{"itervox", "watch"}, false},
		{[]string{"itervox", "status"}, false},
		{[]string{"itervox", "open", "--print"}, false},
		{[]string{"itervox", "--version"}, false},
		{nil, false},
	} {
		assert.Equal(t, tc.want, isDaemonCommandLine(tc.argv), "%q", tc.argv)
	}
}
