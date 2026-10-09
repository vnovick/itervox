package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGHChecks installs a gh whose `pr checks --json` prints out and exits
// with code (gh exits non-zero while checks are pending or failing).
func fakeGHChecks(t *testing.T, out string, code int) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' '" + out + "'\nexit " + string(rune('0'+code)) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestReadPRChecks (#80): buckets are summed whatever gh's exit code, and a
// PR with no checks reported is an error, not "green".
func TestReadPRChecks(t *testing.T) {
	fakeGHChecks(t, `[{"name":"a","bucket":"pass"},{"name":"b","bucket":"skipping"}]`, 0)
	sum, err := ReadPRChecks(context.Background(), "u")
	require.NoError(t, err)
	assert.True(t, sum.Green())

	fakeGHChecks(t, `[{"name":"a","bucket":"pass"},{"name":"b","bucket":"pending"}]`, 8)
	sum, err = ReadPRChecks(context.Background(), "u")
	require.NoError(t, err)
	assert.Equal(t, PRChecks{Total: 2, Passed: 1, Pending: 1}, sum)
	assert.False(t, sum.Green())

	fakeGHChecks(t, `[{"name":"a","bucket":"fail"},{"name":"b","bucket":"cancel"}]`, 1)
	sum, err = ReadPRChecks(context.Background(), "u")
	require.NoError(t, err)
	assert.Equal(t, 2, sum.Failed)

	fakeGHChecks(t, ``, 1)
	_, err = ReadPRChecks(context.Background(), "u")
	require.Error(t, err)

	fakeGHChecks(t, `[]`, 0)
	_, err = ReadPRChecks(context.Background(), "u")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no checks reported")
}
