package tmuxctl

import (
	"errors"
	"slices"
	"testing"
)

// #298: an attach is exactly `attach-session [-r] -t <target>` (plus the
// private socket), never a shell string, never another command or flag.
func TestAttachArgsAreExactlyAttachSession(t *testing.T) {
	cases := []struct {
		name     string
		socket   string
		target   string
		readOnly bool
		want     []string
	}{
		{"read-write session", "", "=itervox-abc123", false, []string{"attach-session", "-t", "=itervox-abc123"}},
		{"read-only session", "", "=itervox-abc123", true, []string{"attach-session", "-r", "-t", "=itervox-abc123"}},
		{"read-only pane", "", "%12", true, []string{"attach-session", "-r", "-t", "%12"}},
		{"private socket", "/tmp/x/s", "%3", false, []string{"-S", "/tmp/x/s", "attach-session", "-t", "%3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Client{Socket: tc.socket}.AttachArgs(tc.target, tc.readOnly)
			if err != nil {
				t.Fatalf("AttachArgs: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("AttachArgs = %q, want %q", got, tc.want)
			}
		})
	}
	for _, bad := range []string{"", "itervox-abc", "=", "=a b", "=a;kill-server", "%", "%1x", "foo:0.1", "-t"} {
		if _, err := (Client{}).AttachArgs(bad, true); !errors.Is(err, ErrBadTarget) {
			t.Errorf("AttachArgs(%q) = %v, want ErrBadTarget", bad, err)
		}
	}
}

// An attach to one of itervox's sessions names it exactly (=name: tmux
// never prefix-matches another session); a hand-started session is reached
// by its pane id.
func TestAttachTarget(t *testing.T) {
	if got, err := AttachTarget("itervox-abc123", "%4"); err != nil || got != "=itervox-abc123" {
		t.Fatalf("AttachTarget(session) = %q, %v", got, err)
	}
	if got, err := AttachTarget("", "%4"); err != nil || got != "%4" {
		t.Fatalf("AttachTarget(pane) = %q, %v", got, err)
	}
	if _, err := AttachTarget("bad name", "4"); !errors.Is(err, ErrBadTarget) {
		t.Fatalf("AttachTarget(bad) = %v, want ErrBadTarget", err)
	}
}

func TestClampSize(t *testing.T) {
	for _, tc := range []struct{ cols, rows, wc, wr int }{
		{0, 0, DefaultTermCols, DefaultTermRows},
		{1, 1, MinTermCols, MinTermRows},
		{80, 24, 80, 24},
		{100000, 100000, MaxTermCols, MaxTermRows},
	} {
		c, r := clampSize(tc.cols, tc.rows)
		if int(c) != tc.wc || int(r) != tc.wr {
			t.Errorf("clampSize(%d,%d) = %d,%d, want %d,%d", tc.cols, tc.rows, c, r, tc.wc, tc.wr)
		}
	}
}

func TestAttachEnvDropsTmuxAndSetsTerm(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-0/default,1,0")
	t.Setenv("TMUX_PANE", "%1")
	t.Setenv("TERM", "dumb")
	env := attachEnv()
	for _, kv := range env {
		if kv == "TERM=dumb" || len(kv) >= 5 && kv[:5] == "TMUX=" || len(kv) >= 10 && kv[:10] == "TMUX_PANE=" {
			t.Fatalf("attachEnv kept %q", kv)
		}
	}
	if !slices.Contains(env, "TERM=xterm-256color") {
		t.Fatalf("attachEnv has no TERM=xterm-256color: %q", env)
	}
}
