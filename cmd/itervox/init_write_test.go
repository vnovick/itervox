package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/workflow"
)

// V5: `itervox init --force` overwrote WORKFLOW.md without the edit lock
// that every other WORKFLOW.md writer takes (CORE-149), so it could land in
// the middle of a running daemon's settings read-modify-write, or of an
// `itervox init --update` migration, and be silently overwritten by it (or
// overwrite it). The write must wait for the lock.
func TestInitForceWriteWaitsForEditLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o644))

	held := make(chan struct{})
	release := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- workflow.WithEditLock(path, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	wrote := make(chan error, 1)
	go func() { wrote <- writeInitWorkflow(path, []byte("new\n")) }()

	select {
	case err := <-wrote:
		close(release)
		<-lockDone
		t.Fatalf("init --force wrote WORKFLOW.md while another writer held the edit lock (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "old\n", string(got))

	close(release)
	require.NoError(t, <-lockDone)
	require.NoError(t, <-wrote)
	got, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "new\n", string(got))
}
