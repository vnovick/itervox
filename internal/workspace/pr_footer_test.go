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

// fakeGHWithBody installs a `gh` that serves one PR body from a file and
// replaces it from stdin on `pr edit --body-file -`.
func fakeGHWithBody(t *testing.T, body string) (bodyFile, calls string) {
	t.Helper()
	dir := t.TempDir()
	bodyFile = filepath.Join(dir, "body")
	calls = filepath.Join(dir, "calls")
	require.NoError(t, os.WriteFile(bodyFile, []byte(body+"\n"), 0o644))
	script := `#!/bin/sh
echo "$*" >> "` + calls + `"
case "$2" in
  view) cat "` + bodyFile + `" ;;
  edit) cat > "` + bodyFile + `" ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bodyFile, calls
}

// TestEnsurePRFooterAppendsOnce (#81): the footer is appended to the
// existing body, and a body that already has it is never edited again.
func TestEnsurePRFooterAppendsOnce(t *testing.T) {
	const url = "https://github.com/o/r/pull/3"
	original := "Fixes #42.\n\nSome `code` with \"quotes\" and $VARS."
	bodyFile, calls := fakeGHWithBody(t, original)

	added, err := EnsurePRFooter(context.Background(), url)
	require.NoError(t, err)
	assert.True(t, added)
	raw, _ := os.ReadFile(bodyFile)
	assert.Equal(t, original+"\n\n---\n"+PRFooterMarker+"\n"+PRFooterText+"\n", string(raw),
		"the original body is kept byte for byte and the footer appended once")

	for range 2 {
		added, err = EnsurePRFooter(context.Background(), url)
		require.NoError(t, err)
		assert.False(t, added, "a body with the footer is not edited again")
	}
	raw, _ = os.ReadFile(bodyFile)
	assert.Equal(t, 1, strings.Count(string(raw), PRFooterMarker))
	assert.Equal(t, 1, strings.Count(readLinesJoined(t, calls), "pr edit"), "one edit across three calls")
}

func TestEnsurePRFooterOnEmptyBody(t *testing.T) {
	bodyFile, _ := fakeGHWithBody(t, "")
	added, err := EnsurePRFooter(context.Background(), "https://github.com/o/r/pull/4")
	require.NoError(t, err)
	assert.True(t, added)
	raw, _ := os.ReadFile(bodyFile)
	assert.Equal(t, "---\n"+PRFooterMarker+"\n"+PRFooterText+"\n", string(raw))
}

func readLinesJoined(t *testing.T, path string) string {
	t.Helper()
	return strings.Join(readLines(t, path), "\n")
}

// TestEnsurePRFooterRecognisesVisibleText (review of #81): a body that kept
// the footer text but lost the hidden marker is not given a second footer.
func TestEnsurePRFooterRecognisesVisibleText(t *testing.T) {
	_, calls := fakeGHWithBody(t, "Body.\n\n---\n"+PRFooterText)
	added, err := EnsurePRFooter(context.Background(), "https://github.com/o/r/pull/5")
	require.NoError(t, err)
	assert.False(t, added)
	assert.NotContains(t, readLinesJoined(t, calls), "pr edit")
}
