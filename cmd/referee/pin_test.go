package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPinMatchesMismatchesAndPrints: pin exits 0 on the pinned hash (any
// case), 20 with the actual hash printed on a different one, and --print
// prints the hash.
func TestPinMatchesMismatchesAndPrints(t *testing.T) {
	file := filepath.Join(t.TempDir(), "WORKFLOW.md")
	content := []byte("human:\n  - \".github/**\"\n")
	require.NoError(t, os.WriteFile(file, content, 0o644))
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])

	code, out, errOut := referee(t, nil, "pin", "--file", file, "--print")
	require.Equal(t, 0, code, errOut)
	assert.Equal(t, want+"  "+file+"\n", out)

	code, out, errOut = referee(t, nil, "pin", "--file", file, "--policy-sha256", strings.ToUpper(want))
	assert.Equal(t, 0, code, out+errOut)
	assert.Contains(t, out, "matches")

	other := strings.Repeat("0", 64)
	code, out, _ = referee(t, nil, "pin", "--file", file, "--policy-sha256", other)
	assert.Equal(t, 20, code)
	assert.Contains(t, out, "actual: "+want)
	assert.Contains(t, out, "pinned: "+other)
}
