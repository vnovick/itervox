//go:build linux

package agent

import (
	"testing"
)

// TestIsolationRealProcessLandlock is the Linux real-process acceptance for
// the Landlock fallback (#258): the same checks as bubblewrap, through the
// re-executed launcher (this test binary handles LandlockEntrypoint in
// TestMain, as itervox's main does). Skipped when the kernel has no
// Landlock.
func TestIsolationRealProcessLandlock(t *testing.T) {
	m := newLandlock()
	if err := m.Available(); err != nil {
		t.Skipf("landlock unavailable: %v", err)
	}
	assertConfined(t, m, newIsolationFixture(t))
}
