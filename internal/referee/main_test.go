package referee

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/gitexec"
)

// run is Run with the standalone program name, for the tests.
func run(ctx context.Context, args []string, env envFunc, stdout, stderr io.Writer) int {
	return dispatch(ctx, "referee", args, env, stdout, stderr)
}

func TestMain(m *testing.M) {
	gitexec.UnsetInProcess()
	os.Exit(m.Run())
}

// testRepo is a throwaway git repository with a main branch.
type testRepo struct {
	t   *testing.T
	dir string
}

func newTestRepo(t *testing.T) testRepo {
	t.Helper()
	r := testRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.email", "referee@example.invalid")
	r.git("config", "user.name", "referee test")
	r.git("config", "commit.gpgsign", "false")
	return r
}

func (r testRepo) git(args ...string) string {
	r.t.Helper()
	out, err := gitexec.Command(context.Background(), r.dir, args...).CombinedOutput()
	require.NoError(r.t, err, "git %v: %s", args, out)
	return string(out)
}

// write writes files (path -> content; "" content deletes the file).
func (r testRepo) write(files map[string]string) {
	r.t.Helper()
	for p, content := range files {
		full := filepath.Join(r.dir, filepath.FromSlash(p))
		if content == "" {
			require.NoError(r.t, os.Remove(full))
			continue
		}
		require.NoError(r.t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(r.t, os.WriteFile(full, []byte(content), 0o644))
	}
}

// commit writes files and commits everything.
func (r testRepo) commit(msg string, files map[string]string) {
	r.t.Helper()
	r.write(files)
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
}

// referee runs a subcommand with env and returns its exit code and output.
func referee(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, func(k string) string { return env[k] }, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}
