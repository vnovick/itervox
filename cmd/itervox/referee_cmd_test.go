package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/gitexec"
)

// TestItervoxRefereeLanesHumanLane runs `itervox referee lanes` on a branch
// that edits a workflow file: the human lane, exit 20, and the usage text
// names the itervox front end.
func TestItervoxRefereeLanesHumanLane(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "referee@example.invalid")
	git("config", "user.name", "referee test")
	git("config", "commit.gpgsign", "false")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	write("cart.go", "package cart\n")
	write(".github/workflows/ci.yml", "jobs: {}\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "fix")
	write(".github/workflows/ci.yml", "jobs: {}\ncontinue-on-error: true\n")
	git("commit", "-q", "-am", "agent fix")

	t.Chdir(dir)
	var out, errOut bytes.Buffer
	code := refereeCmd(context.Background(), []string{"lanes", "--base", "main"}, func(string) string { return "" }, &out, &errOut)
	require.Equal(t, 20, code, "stdout=%s stderr=%s", out.String(), errOut.String())
	require.Contains(t, out.String(), ".github/workflows/ci.yml")

	out.Reset()
	errOut.Reset()
	require.Equal(t, 2, refereeCmd(context.Background(), nil, func(string) string { return "" }, &out, &errOut))
	require.Contains(t, errOut.String(), "usage: itervox referee <command>")
}

// TestItervoxRefereeRecordNamesTheBuild: `itervox referee lanes --key
// --record` signs a referee record naming this binary's ldflags version and
// commit.
func TestItervoxRefereeRecordNamesTheBuild(t *testing.T) {
	oldV, oldC := version, commit
	t.Cleanup(func() { version, commit = oldV, oldC })
	version, commit = "0.3.4-itervox-test", "1234567890abcdef1234567890abcdef12345678"

	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.email", "referee@example.invalid"},
		{"config", "user.name", "referee test"}, {"config", "commit.gpgsign", "false"},
		{"commit", "-q", "--allow-empty", "-m", "base"},
	} {
		out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	keys, records := t.TempDir(), filepath.Join(t.TempDir(), "referee.jsonl")
	env := func(string) string { return "" }
	var out, errOut bytes.Buffer
	require.Equal(t, 0, refereeCmd(context.Background(), []string{"keygen", "--out", keys}, env, &out, &errOut), errOut.String())
	require.Equal(t, 0, refereeCmd(context.Background(), []string{"lanes", "--dir", dir, "--base", "main",
		"--key", filepath.Join(keys, attest.KeyFileName), "--record", records}, env, &out, &errOut), errOut.String())
	recs, err := attest.ReadFile(records)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.NotNil(t, recs[0].Referee)
	require.Equal(t, version, recs[0].Referee.Version)
	require.Equal(t, commit, recs[0].Referee.Commit)
	require.Equal(t, "lanes", recs[0].Referee.Command)
}
