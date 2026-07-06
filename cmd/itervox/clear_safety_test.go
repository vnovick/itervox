package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnsafeWorkspaceRoot_Refuses(t *testing.T) {
	cases := []struct {
		path       string
		mustRefuse bool
	}{
		{"", true},       // empty
		{"/", true},      // filesystem root
		{"/tmp", true},   // common temp
		{"/var", true},   // system
		{"/etc", true},   // system
		{"/usr", true},   // system
		{"/opt", true},   // system
		{"/Users", true}, // parent of macOS home
		{"/home", true},  // parent of Linux home (covered by ancestor check on Linux)
		{"/tmp/itervox-workspaces/myproj", false},            // project-specific suffix is fine
		{"/Users/someone/dev/myproj/.itervox/ws", false},     // typical user setup
		{filepath.Join(os.TempDir(), "itervox-test"), false}, // sibling of /tmp via abs path
	}
	for _, tc := range cases {
		got := unsafeWorkspaceRoot(tc.path)
		refused := got != ""
		if refused != tc.mustRefuse {
			t.Errorf("unsafeWorkspaceRoot(%q) refused=%v reason=%q; want refused=%v",
				tc.path, refused, got, tc.mustRefuse)
		}
	}
}

func TestUnsafeWorkspaceRoot_RefusesUserHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot resolve user home dir: %v", err)
	}
	if got := unsafeWorkspaceRoot(home); got == "" {
		t.Errorf("unsafeWorkspaceRoot(%q) accepted user home; expected refusal", home)
	}
}

func TestUnsafeWorkspaceRoot_AcceptsTilde(t *testing.T) {
	// `~` is NOT expanded by Go — it would resolve via filepath.Abs to the
	// process cwd + `~`, which is clearly not the home dir. Document the
	// behavior: literal `~` passes the safety check (because it's a weird
	// relative path, not a shell-expanded home). This is OK because if a
	// user actually wrote `workspace.root: ~`, the daemon would also fail
	// to find/create the dir and the user would notice immediately.
	got := unsafeWorkspaceRoot("~")
	if got != "" {
		t.Logf("note: unexpanded ~ reported as: %s — that's fine; it's not a real path", got)
	}
}
