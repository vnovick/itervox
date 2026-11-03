package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// TestTokenCLICreateListRotateRevoke (#214): `itervox token` works on the
// file with no daemon; a new secret is printed once, alone on stdout, and
// only its hash is stored.
func TestTokenCLICreateListRotateRevoke(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "WORKFLOW.md")
	now := time.Now()
	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := tokenCmd(append(args, "--workflow", wf), &out, &errOut, now)
		return code, out.String(), errOut.String()
	}

	code, _, errOut := run("rotate")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "no stored admin token")

	code, secret, errOut := run("create", "--name", "teammate", "--scope", "viewer", "--expires", "30d")
	require.Equal(t, 0, code, errOut)
	secret = strings.TrimSpace(secret)
	require.True(t, strings.HasPrefix(secret, apitokens.SecretPrefix), "stdout is the secret alone")
	assert.NotContains(t, errOut, secret, "the secret is never on stderr")
	assert.Contains(t, errOut, "shown once")

	code, adminSecret, _ := run("create", "--name", "admin", "--scope", "admin")
	require.Equal(t, 0, code)
	adminSecret = strings.TrimSpace(adminSecret)

	raw, err := os.ReadFile(filepath.Join(dir, ".itervox", apitokens.FileName))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), secret)
	assert.NotContains(t, string(raw), adminSecret)

	code, list, _ := run("list")
	require.Equal(t, 0, code)
	assert.Contains(t, list, "teammate")
	assert.Contains(t, list, "viewer")
	assert.NotContains(t, list, secret)

	// rotate with no ID rotates the one stored admin token.
	code, rotated, errOut := run("rotate")
	require.Equal(t, 0, code, errOut)
	rotated = strings.TrimSpace(rotated)
	store := apitokens.Open(filepath.Join(dir, ".itervox", apitokens.FileName))
	_, ok := store.Authenticate(adminSecret)
	assert.False(t, ok, "the old admin secret is dead")
	got, ok := store.Authenticate(rotated)
	require.True(t, ok)
	assert.Equal(t, apitokens.ScopeAdmin, got.Scope)

	viewer, ok := store.Authenticate(secret)
	require.True(t, ok)
	code, out, _ := run("revoke", viewer.ID)
	require.Equal(t, 0, code)
	assert.Contains(t, out, "revoked "+viewer.ID)
	_, ok = apitokens.Open(store.FilePath()).Authenticate(secret)
	assert.False(t, ok)

	code, _, _ = run("create", "--name", "x", "--scope", "root")
	assert.Equal(t, 1, code)
	code, _, _ = run("revoke")
	assert.Equal(t, 2, code)
	code, _, _ = run("bogus")
	assert.Equal(t, 2, code)
}

// TestItervoxGitignoreIgnoresTokenStore: .itervox/.gitignore keeps the token
// store and its lock out of git.
func TestItervoxGitignoreIgnoresTokenStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".itervox")
	require.NoError(t, ensureItervoxGitignore(dir))
	raw, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	lines := strings.Split(string(raw), "\n")
	assert.Contains(t, lines, "/tokens.json")
	assert.Contains(t, lines, "/tokens.json.lock")
}
