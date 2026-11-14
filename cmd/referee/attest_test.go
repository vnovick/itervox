package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/attest"
)

// TestKeygenRunVerify: keygen writes a 0600 private key; run records a
// passing check and a failing pipeline (pipefail: the real exit code, not
// tail's); verify accepts the records, with --tree on the unchanged tree,
// and rejects a tampered record and a tree that changed since.
func TestKeygenRunVerify(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("checks are bash command lines")
	}
	r := newTestRepo(t)
	r.commit("base", map[string]string{"main.go": "package main\n"})
	keys := t.TempDir()

	code, out, errOut := referee(t, nil, "keygen", "--out", keys)
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "key id")
	fi, err := os.Stat(filepath.Join(keys, attest.KeyFileName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	code, _, errOut = referee(t, nil, "keygen", "--out", keys)
	assert.Equal(t, 2, code, "never overwrites a key")
	assert.Contains(t, errOut, "already exists")

	key := filepath.Join(keys, attest.KeyFileName)
	pub := filepath.Join(keys, attest.PublicKeyFileName)
	records := filepath.Join(r.dir, "records.jsonl") // inside the tree: excluded from its hash

	code, out, errOut = referee(t, nil, "run", "--dir", r.dir, "--name", "test", "--key", key, "--out", records, "--", "echo ok")
	require.Equal(t, 0, code, out+errOut)
	code, out, errOut = referee(t, nil, "run", "--dir", r.dir, "--name", "pipe", "--key", key, "--out", records, "--", "exit 3 | tail -n 1")
	assert.Equal(t, 3, code, "pipefail: the failing stage's status, not tail's: "+out+errOut)
	code, _, _ = referee(t, nil, "run", "--dir", r.dir, "--name", "argv", "--key", key, "--out", records, "--", "false", "ignored-arg")
	assert.Equal(t, 1, code, "an argv command runs without a shell")

	recs, err := attest.ReadFile(records)
	require.NoError(t, err)
	require.Len(t, recs, 6, "a check and a seal per run")
	assert.Equal(t, 3, recs[2].ExitCode)
	assert.True(t, recs[2].Pipefail)

	code, out, errOut = referee(t, nil, "verify", records, "--pubkey", pub, "--tree", r.dir)
	require.Equal(t, 0, code, out+errOut)
	assert.Contains(t, out, "all 6 records verify")

	raw, err := os.ReadFile(records)
	require.NoError(t, err)
	tampered := filepath.Join(t.TempDir(), "tampered.jsonl")
	require.NoError(t, os.WriteFile(tampered, []byte(strings.Replace(string(raw), `"exit_code":3`, `"exit_code":0`, 1)), 0o644))
	code, out, _ = referee(t, nil, "verify", "--pubkey", pub, tampered)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "signature does not verify")

	r.write(map[string]string{"main.go": "package main // changed after the checks\n"})
	code, out, _ = referee(t, nil, "verify", records, "--pubkey", pub, "--tree", r.dir)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "different tree")

	// A key inside the tree the check runs in is refused.
	inside := filepath.Join(r.dir, "k")
	require.NoError(t, os.MkdirAll(inside, 0o700))
	code, _, errOut = referee(t, nil, "keygen", "--out", inside)
	require.Equal(t, 0, code, errOut)
	code, _, errOut = referee(t, nil, "run", "--dir", r.dir, "--name", "x", "--key", filepath.Join(inside, attest.KeyFileName), "--out", records, "--", "true")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "inside the working tree")
}
