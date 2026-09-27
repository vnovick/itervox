//go:build sshmatrix

package agent_test

import "testing"

// sshMatrixEnabled: built with -tags sshmatrix (see sshmatrix_off_test.go).
const sshMatrixEnabled = true

// requireSSHMatrix is a no-op when built with -tags sshmatrix.
func requireSSHMatrix(t *testing.T) { t.Helper() }
