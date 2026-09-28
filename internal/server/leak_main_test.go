package server

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package's test run when a goroutine is still running
// after every test finished (CORE-112). The ignore list is empty: an
// exemption must name one exact top function (goleak.IgnoreTopFunction) with
// a one-line lifecycle reason — no package-wide or prefix exemptions.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
