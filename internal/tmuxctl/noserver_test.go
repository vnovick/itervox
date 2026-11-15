package tmuxctl

import "testing"

// A tmux client that reaches a server while it shuts down (its last
// session was just killed) prints "server exited unexpectedly"; that is
// "no server", not a failure, so HasSession reports false instead of an
// error after Force stop ends the last session.
func TestNoServerRecognisesAServerShuttingDown(t *testing.T) {
	for _, msg := range []string{
		"no server running on /tmp/tmux-0/default",
		"error connecting to /tmp/tmux-0/default (No such file or directory)",
		"server exited unexpectedly",
		"no current target",
	} {
		if !noServer(msg) {
			t.Errorf("noServer(%q) = false, want true", msg)
		}
	}
	for _, msg := range []string{"can't find session: x", "unknown command: foo", ""} {
		if noServer(msg) {
			t.Errorf("noServer(%q) = true, want false", msg)
		}
	}
}
