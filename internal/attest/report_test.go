package attest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadKeyNeverCreatesAndAppendFileRoundTrips: LoadKey reads an
// existing key and fails on a missing one without creating it; AppendFile
// appends records (0600) that ReadFile reads back in order.
func TestLoadKeyNeverCreatesAndAppendFileRoundTrips(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, KeyFileName)
	_, err := LoadKey(missing)
	require.Error(t, err)
	_, statErr := os.Stat(missing)
	assert.True(t, os.IsNotExist(statErr), "LoadKey must not create a key")

	created, err := LoadOrCreateKey(missing)
	require.NoError(t, err)
	loaded, err := LoadKey(missing)
	require.NoError(t, err)
	assert.Equal(t, KeyID(created.PublicKey()), KeyID(loaded.PublicKey()))

	path := filepath.Join(dir, "records.jsonl")
	require.NoError(t, AppendFile(path, Record{Version: RecordVersion, Kind: KindCheck, Name: "a"}))
	require.NoError(t, AppendFile(path, Record{Version: RecordVersion, Kind: KindSeal}))
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	recs, err := ReadFile(path)
	require.NoError(t, err)
	require.Len(t, recs, 2)
	assert.Equal(t, "a", recs[0].Name)
	assert.Equal(t, KindSeal, recs[1].Kind)
	assert.Equal(t, "-", ShortHex(""))
	assert.Equal(t, "0123456789ab", ShortHex("0123456789abcdef"))
}
