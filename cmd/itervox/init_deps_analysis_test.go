package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitEnvLooksLikePlaceholder_MissingFileReportsPlaceholder(t *testing.T) {
	dir := t.TempDir()
	assert.True(t, initEnvLooksLikePlaceholder(filepath.Join(dir, "missing")),
		"missing .env file is treated as placeholder so init skips the analysis pass")
}

func TestInitEnvLooksLikePlaceholder_StubMatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte("LINEAR_API_KEY=lin_api_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"), 0o644))
	assert.True(t, initEnvLooksLikePlaceholder(path))
}

func TestInitEnvLooksLikePlaceholder_RealKeyDoesNotMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte("LINEAR_API_KEY=lin_api_realtoken1234567890\n"), 0o644))
	assert.False(t, initEnvLooksLikePlaceholder(path),
		"a real (non-placeholder) credential must not be treated as a stub")
}
