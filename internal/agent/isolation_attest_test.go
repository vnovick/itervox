package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/attest"
)

// TestAttestKeyDeniedToIsolatedAgent (signed checks): the daemon's signing
// key, .itervox/<attest.KeyFileName> next to WORKFLOW.md, is in
// IsolationProjectDeny, and a run's sandbox policy denies the real file, so
// an agent under isolation: process cannot read it to forge a record.
func TestAttestKeyDeniedToIsolatedAgent(t *testing.T) {
	rel := filepath.Join(".itervox", attest.KeyFileName)
	assert.Contains(t, IsolationProjectDeny, filepath.ToSlash(rel), "the signing key is in the project deny list")

	project := t.TempDir()
	keyPath := filepath.Join(project, rel)
	_, err := attest.LoadOrCreateKey(keyPath)
	require.NoError(t, err)
	ws := t.TempDir()

	policy := BuildIsolationPolicy(IsolationInputs{Workspace: ws, ProjectDir: project})
	realKey, err := filepath.EvalSymlinks(keyPath)
	require.NoError(t, err)
	assert.True(t, slices.Contains(policy.Deny, realKey), "the policy denies %s (deny: %v)", realKey, policy.Deny)
	pub, err := filepath.EvalSymlinks(filepath.Join(project, ".itervox", attest.PublicKeyFileName))
	require.NoError(t, err)
	assert.False(t, slices.Contains(policy.Deny, pub), "the public key stays readable")
	_, err = os.Stat(keyPath)
	require.NoError(t, err)
}
