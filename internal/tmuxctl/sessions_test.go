package tmuxctl

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Session tags round-trip through tmux user options: set on one session,
// listed with list-sessions -F, absent (empty) on another; HasSession
// matches the exact name only. Real tmux on a private socket.
func TestTmuxSessionTagsRoundTrip(t *testing.T) {
	dir, script := fakeAgent(t)
	c := privateServer(t, "itervox-abc123", dir, script)
	ctx := context.Background()
	if err := c.NewSession(ctx, "itervox-abc1234", dir, script); err != nil {
		t.Fatal(err)
	}
	project := "/home/me/my project"
	err := c.SetSessionOptions(ctx, "itervox-abc123", map[string]string{
		OptProject: project, OptBackend: "claude", OptCwd: dir, OptGuarded: "1", OptProfile: "",
	})
	if err != nil {
		t.Fatalf("set options: %v", err)
	}
	list, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]SessionInfo{}
	for _, s := range list {
		byName[s.Name] = s
	}
	got, ok := byName["itervox-abc123"]
	if !ok {
		t.Fatalf("tagged session not listed: %+v", list)
	}
	if got.Project != project || got.Backend != "claude" || got.StartCwd != dir || !got.Guarded || got.Profile != "" {
		t.Fatalf("tags = %+v", got)
	}
	if time.Since(got.Created) > time.Minute {
		t.Fatalf("created = %v", got.Created)
	}
	if other := byName["itervox-abc1234"]; other.Project != "" || other.Guarded {
		t.Fatalf("untagged session carries tags: %+v", other)
	}

	for name, want := range map[string]bool{"itervox-abc123": true, "itervox-abc": false, "nope": false} {
		has, err := c.HasSession(ctx, name)
		if err != nil || has != want {
			t.Fatalf("HasSession(%q) = %v, %v; want %v", name, has, err, want)
		}
	}
}

// Only @itervox-* options may be set, and values are single-line.
func TestTmuxSetSessionOptionsRejectsForeignOptions(t *testing.T) {
	c := Client{Socket: "/nonexistent/socket"}
	ctx := context.Background()
	for _, opts := range []map[string]string{
		{"status": "off"},
		{"@other": "x"},
		{OptProject: "/a\n/b"},
	} {
		if err := c.SetSessionOptions(ctx, "itervox-abc123", opts); !errors.Is(err, ErrBadTarget) {
			t.Fatalf("SetSessionOptions(%v) = %v, want ErrBadTarget", opts, err)
		}
	}
	if err := c.SetSessionOptions(ctx, "bad name", nil); !errors.Is(err, ErrBadTarget) {
		t.Fatalf("bad name: %v", err)
	}
}
