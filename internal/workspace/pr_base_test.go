package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGHWithBase installs a `gh` on PATH that serves a single PR whose base
// branch lives in a file: `pr view --json baseRefName` prints it, and
// `pr edit --base X` rewrites it. Every invocation is appended to calls.
func fakeGHWithBase(t *testing.T, base string, editFails bool) (calls, baseFile string) {
	t.Helper()
	dir := t.TempDir()
	baseFile = filepath.Join(dir, "base")
	calls = filepath.Join(dir, "calls")
	require.NoError(t, os.WriteFile(baseFile, []byte(base+"\n"), 0o644))
	editBody := `printf '%s\n' "$5" > "` + baseFile + `"`
	if editFails {
		editBody = `echo "GraphQL: Base ref must be a branch" >&2; exit 1`
	}
	script := `#!/bin/sh
echo "$*" >> "` + calls + `"
case "$2" in
  view) cat "` + baseFile + `" ;;
  edit) ` + editBody + ` ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls, baseFile
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// TestSetPRBaseEditsOnlyWhenDifferent (#73): a PR on the wrong base is edited
// with `gh pr edit --base`; one already on the requested base is left alone.
func TestSetPRBaseEditsOnlyWhenDifferent(t *testing.T) {
	const url = "https://github.com/o/r/pull/7"
	calls, baseFile := fakeGHWithBase(t, "main", false)

	changed, err := SetPRBase(context.Background(), url, "itervox/ENG-1")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, []string{"itervox/ENG-1"}, readLines(t, baseFile))
	assert.Equal(t, []string{
		"pr view " + url + " --json baseRefName --jq .baseRefName",
		"pr edit " + url + " --base itervox/ENG-1",
	}, readLines(t, calls))

	changed, err = SetPRBase(context.Background(), url, "itervox/ENG-1")
	require.NoError(t, err)
	assert.False(t, changed, "already on the requested base: no edit")
	assert.Len(t, readLines(t, calls), 3, "only a view was added")
}

// TestSetPRBaseReportsEditFailure: GitHub rejecting the base (e.g. the branch
// is not on the remote) is returned with gh's message.
func TestSetPRBaseReportsEditFailure(t *testing.T) {
	fakeGHWithBase(t, "main", true)
	changed, err := SetPRBase(context.Background(), "https://github.com/o/r/pull/7", "itervox/ENG-1")
	assert.False(t, changed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Base ref must be a branch")
}

func TestSetPRBaseNoopOnEmptyInput(t *testing.T) {
	calls, _ := fakeGHWithBase(t, "main", false)
	for _, in := range [][2]string{{"", "main"}, {"https://github.com/o/r/pull/7", ""}} {
		changed, err := SetPRBase(context.Background(), in[0], in[1])
		assert.NoError(t, err)
		assert.False(t, changed)
	}
	assert.Empty(t, readLines(t, calls))
}
