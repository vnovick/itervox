package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// M4-close D9 / BH-M4-1 — the doctor --deploy push probe is read-only: it
// must not run the repository's pre-push hook (lefthook/husky suites are
// arbitrary code, minutes long, and a failing hook would report as a
// [fail] push auth). A real git repo with a local bare remote and a
// pre-push hook that writes a marker file; after the probe the marker must
// not exist.
func TestDoctorPushProbeSkipsPrePushHook(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	marker := filepath.Join(root, "HOOK_RAN")
	gitRun := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun(root, "init", "-q", "--bare", remote)
	gitRun(root, "init", "-q", work)
	gitRun(work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "x")
	gitRun(work, "remote", "add", "origin", remote)
	hook := "#!/bin/sh\necho ran > '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(work, ".git", "hooks", "pre-push"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	env := defaultDeployProbeEnv()
	env.timeout = 20 * time.Second
	c := gitPushCheck(context.Background(), work, env)

	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("the pre-push hook ran during the read-only doctor probe (check: %s %s)", c.Status, c.Detail)
	}
	if c.Status != deployOK {
		t.Fatalf("want push auth ok against a local remote, got %s: %s", c.Status, c.Detail)
	}
}
