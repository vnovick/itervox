//go:build !sshmatrix

package agent_test

import "testing"

// sshMatrixEnabled gates the slow SSH login-shell matrix tests (CORE-172).
// They take ~400 s of this package's ~590 s under -race, which pushed a plain
// `go test ./...` past go test's default 10-minute per-package timeout. They
// still run in `make test` / `make verify` and in CI, which pass
// `-tags sshmatrix`; a plain `go test` skips them with a pointer to the tag.
const sshMatrixEnabled = false

// requireSSHMatrix skips the calling test unless built with -tags sshmatrix.
func requireSSHMatrix(t *testing.T) {
	t.Helper()
	if !sshMatrixEnabled {
		t.Skip("SSH login-shell matrix: run with -tags sshmatrix (make test and CI do)")
	}
}
