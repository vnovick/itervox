package tmuxctl

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// privateServer starts a tmux server on a private socket (never the
// default one) with one session running script in dir, and kills it when
// the test ends. Skips when tmux is not installed.
func privateServer(t *testing.T, name, dir, script string) Client {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	sockDir, err := os.MkdirTemp("", "tmx")
	if err != nil {
		t.Fatal(err)
	}
	c := Client{Socket: filepath.Join(sockDir, "s")}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", c.Socket, "kill-server").Run()
		_ = os.RemoveAll(sockDir)
	})
	if err := c.NewSession(context.Background(), name, dir, script); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	return c
}

// fakeAgent writes a script that echoes every line it reads.
func fakeAgent(t *testing.T) (dir, script string) {
	t.Helper()
	dir = t.TempDir()
	script = filepath.Join(dir, "agent.sh")
	body := "#!/bin/sh\necho ready\nwhile IFS= read -r line; do echo \"got: $line\"; done\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, script
}

func waitScreen(t *testing.T, c Client, pane, want string) string {
	t.Helper()
	var last string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := c.Capture(context.Background(), pane, 50)
		if err == nil && strings.Contains(out, want) {
			return out
		}
		last = out
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q; last capture:\n%s", want, last)
	return ""
}

// #178: text pasted into a pane through a private buffer, then Enter,
// reaches the program in it, and capture-pane shows the result. Real tmux
// on a private socket.
func TestTmuxSendTextAndCaptureRoundTrip(t *testing.T) {
	dir, script := fakeAgent(t)
	c := privateServer(t, "itervox-test1", dir, script)
	ctx := context.Background()
	panes, err := c.ListPanes(ctx)
	if err != nil || len(panes) != 1 {
		t.Fatalf("ListPanes = %v, %v", panes, err)
	}
	p := panes[0]
	if p.Session != "itervox-test1" || p.Label() != "itervox-test1:0.0" || !ValidPaneID(p.ID) || p.PID <= 0 {
		t.Fatalf("pane = %+v", p)
	}
	if canonical(p.Cwd) != canonical(dir) {
		t.Fatalf("pane cwd = %q, want %q", p.Cwd, dir)
	}
	waitScreen(t, c, p.ID, "ready")
	if err := c.SendText(ctx, p.ID, "hello 'quoted' $HOME world", true); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	waitScreen(t, c, p.ID, "got: hello 'quoted' $HOME world")

	// Without submit the text waits on the line; Enter submits it.
	if err := c.SendText(ctx, p.ID, "second", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if out, _ := c.Capture(ctx, p.ID, 50); strings.Contains(out, "got: second") {
		t.Fatalf("text was submitted without Enter:\n%s", out)
	}
	if err := c.SendKey(ctx, p.ID, "Enter"); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, c, p.ID, "got: second")

	// The paste buffer is single-use: nothing is left behind.
	out, err := c.run(ctx, "", "list-buffers")
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("buffers left behind: %q (%v)", out, err)
	}
	if err := c.KillSession(ctx, "itervox-test1"); err != nil {
		t.Fatal(err)
	}
	if err := c.SendText(ctx, p.ID, "gone", true); !errors.Is(err, ErrNoSuchPane) {
		t.Fatalf("SendText to a killed pane = %v, want ErrNoSuchPane", err)
	}
}

// Only the allowlisted keys are ever sent; nothing else reaches tmux.
func TestTmuxSendKeyRejectsKeysOutsideAllowlist(t *testing.T) {
	for _, k := range []string{"C-d", "C-z", "q", "0", "10", "Enter Enter", "-l", "BSpace", "M-x", "", "y"} {
		if AllowedKey(k) {
			t.Errorf("AllowedKey(%q) = true", k)
		}
		// No tmux call is made: the empty client has no server to reach.
		if err := (Client{Socket: "/nonexistent/sock"}).SendKey(context.Background(), "%1", k); !errors.Is(err, ErrKeyNotAllowed) {
			t.Errorf("SendKey(%q) = %v, want ErrKeyNotAllowed", k, err)
		}
	}
	for _, k := range []string{"Enter", "Escape", "Up", "Down", "Left", "Right", "Tab", "C-c", "1", "5", "9"} {
		if !AllowedKey(k) {
			t.Errorf("AllowedKey(%q) = false", k)
		}
	}
	// Targets tmux could misread are refused before any call.
	if err := (Client{}).SendKey(context.Background(), "-t", "Enter"); !errors.Is(err, ErrBadTarget) {
		t.Errorf("bad pane id = %v", err)
	}
	if err := (Client{}).NewSession(context.Background(), "a;b", "/", "x"); !errors.Is(err, ErrBadTarget) {
		t.Errorf("bad session name = %v", err)
	}
}

// A pane is matched to a CLI process when the process is the pane's own or
// a descendant of it AND the pane's directory is the session's.
func TestMatchPaneByAncestryAndDirectory(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	panes := []Pane{
		{ID: "%1", PID: 100, Cwd: other, Session: "a"},
		{ID: "%2", PID: 200, Cwd: dir, Session: "b"},
		{ID: "%3", PID: 300, Cwd: dir, Session: "c"},
	}
	if p, ok := MatchPane(panes, 250, []int{210, 200, 1}, dir); !ok || p.ID != "%2" {
		t.Fatalf("descendant = %+v %v", p, ok)
	}
	if p, ok := MatchPane(panes, 300, nil, dir); !ok || p.ID != "%3" {
		t.Fatalf("pane's own process = %+v %v", p, ok)
	}
	if _, ok := MatchPane(panes, 150, []int{100}, dir); ok {
		t.Fatal("matched a pane in another directory")
	}
	if _, ok := MatchPane(panes, 999, []int{998}, dir); ok {
		t.Fatal("matched a pane merely in the same directory")
	}
	if _, ok := MatchPane(panes, 0, nil, dir); ok {
		t.Fatal("matched without a process")
	}
}

// No tmux binary: an empty pane list, no error.
func TestListPanesWithoutTmuxIsEmpty(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	panes, err := (Client{}).ListPanes(context.Background())
	if err != nil || len(panes) != 0 {
		t.Fatalf("ListPanes = %v, %v", panes, err)
	}
	if _, err := (Client{}).Version(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Version = %v", err)
	}
}

// TestServerSocketResolvesLikeTmux (#210): the socket a client talks to,
// and ServerMayRun's no-fork answer from it.
func TestServerSocketResolvesLikeTmux(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", dir)
	def := filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()), "default")
	if got := (Client{}).ServerSocket(); got != def {
		t.Fatalf("default socket = %q, want %q", got, def)
	}
	if (Client{}).ServerMayRun() {
		t.Fatal("no socket: ServerMayRun must be false")
	}
	if err := os.MkdirAll(filepath.Dir(def), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(def, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !(Client{}).ServerMayRun() {
		t.Fatal("socket present: ServerMayRun must be true")
	}
	t.Setenv("TMUX", "/run/other.sock,123,0")
	if got := (Client{}).ServerSocket(); got != "/run/other.sock" {
		t.Fatalf("inside tmux: socket = %q", got)
	}
	if got := (Client{Socket: "/x/s"}).ServerSocket(); got != "/x/s" {
		t.Fatalf("explicit socket = %q", got)
	}
}
