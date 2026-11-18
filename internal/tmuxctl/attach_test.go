package tmuxctl

import (
	"errors"
	"slices"
	"testing"
)

// #298: an attach is exactly `attach-session [-r [-f ignore-size]] -t
// <target>` (plus the private socket), never a shell string, never another
// command or flag. ignore-size goes only with -r: a read-write client
// sizes the window like any other.
func TestAttachArgsAreExactlyAttachSession(t *testing.T) {
	ro := AttachMode{ReadOnly: true}
	roIgnore := AttachMode{ReadOnly: true, IgnoreSize: true}
	cases := []struct {
		name   string
		socket string
		target string
		mode   AttachMode
		want   []string
	}{
		{"read-write session", "", "=itervox-abc123", AttachMode{}, []string{"attach-session", "-t", "=itervox-abc123"}},
		{"read-write ignores IgnoreSize", "", "=itervox-abc123", AttachMode{IgnoreSize: true}, []string{"attach-session", "-t", "=itervox-abc123"}},
		{"read-only session (old tmux)", "", "=itervox-abc123", ro, []string{"attach-session", "-r", "-t", "=itervox-abc123"}},
		{"read-only session", "", "=itervox-abc123", roIgnore, []string{"attach-session", "-r", "-f", "ignore-size", "-t", "=itervox-abc123"}},
		{"read-only pane", "", "%12", roIgnore, []string{"attach-session", "-r", "-f", "ignore-size", "-t", "%12"}},
		{"private socket", "/tmp/x/s", "%3", AttachMode{}, []string{"-S", "/tmp/x/s", "attach-session", "-t", "%3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Client{Socket: tc.socket}.AttachArgs(tc.target, tc.mode)
			if err != nil {
				t.Fatalf("AttachArgs: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("AttachArgs = %q, want %q", got, tc.want)
			}
		})
	}
	for _, bad := range []string{"", "itervox-abc", "=", "=a b", "=a;kill-server", "%", "%1x", "foo:0.1", "-t"} {
		if _, err := (Client{}).AttachArgs(bad, roIgnore); !errors.Is(err, ErrBadTarget) {
			t.Errorf("AttachArgs(%q) = %v, want ErrBadTarget", bad, err)
		}
	}
}

// -f (client flags) exists from tmux 3.2 (OpenBSD 6.9); an older or
// unreadable version gets plain -r, since -f would end the attach.
func TestClientFlagsSupported(t *testing.T) {
	for v, want := range map[string]bool{
		"tmux 3.4": true, "tmux 3.2": true, "tmux 3.2a": true, "tmux 3.10": true, "tmux 4.0": true,
		"tmux next-3.6": true, "tmux master": true, "tmux openbsd-7.5": true, "tmux openbsd-6.9": true,
		"tmux 3.1c": false, "tmux 3.0a": false, "tmux 2.8": false, "tmux openbsd-6.8": false,
		"": false, "tmux": false, "tmux x.y": false, "garbage": false,
	} {
		if got := ClientFlagsSupported(v); got != want {
			t.Errorf("ClientFlagsSupported(%q) = %v, want %v", v, got, want)
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
