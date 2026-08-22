package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureEnvStubCreatesAndNeverClobbers pins the file `itervox init
// --update` was leaving out.
//
// --update returns early after migrating the workflow schema, so it never
// reached the fresh-init path that writes .itervox/.env. An existing project
// migrating to schema 2 was therefore left with `api_key: $LINEAR_API_KEY`
// and no file to define it in, and the daemon hard-failed startup with
// "missing tracker.api_key: must be set or resolved from $VAR" — accurate,
// but with no indication of where the variable was supposed to live.
func TestEnsureEnvStubCreatesAndNeverClobbers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".itervox")

	ensureEnvStub(dir, "linear")
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	require.NoError(t, err, "the stub must exist after --update")
	assert.Contains(t, string(data), "LINEAR_API_KEY")

	// Real credentials must survive a re-run — init is expected to be
	// idempotent and operators re-run --update after every schema bump.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("LINEAR_API_KEY=lin_api_real_secret\n"), 0o600))
	ensureEnvStub(dir, "linear")
	after, err := os.ReadFile(filepath.Join(dir, ".env"))
	require.NoError(t, err)
	assert.Contains(t, string(after), "lin_api_real_secret",
		"an existing .env must never be overwritten")
}

func TestEnsureEnvStubTrackerKinds(t *testing.T) {
	for _, tc := range []struct{ kind, want string }{
		{"linear", "LINEAR_API_KEY"},
		{"github", "GITHUB_TOKEN"},
	} {
		dir := filepath.Join(t.TempDir(), ".itervox")
		ensureEnvStub(dir, tc.kind)
		data, err := os.ReadFile(filepath.Join(dir, ".env"))
		require.NoError(t, err)
		assert.Contains(t, string(data), tc.want)
	}

	// An undeterminable tracker kind still gets a file: the operator needs a
	// place to put credentials more than they need a guessed variable name.
	dir := filepath.Join(t.TempDir(), ".itervox")
	ensureEnvStub(dir, "")
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "Itervox environment")
}

// TestEnsureEnvStubIsNotWorldReadable — the file holds an API key.
func TestEnsureEnvStubIsNotWorldReadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".itervox")
	ensureEnvStub(dir, "linear")
	info, err := os.Stat(filepath.Join(dir, ".env"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"a credentials file must not be readable by other users")
}
