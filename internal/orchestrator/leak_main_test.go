package orchestrator

import (
	"testing"

	"go.uber.org/goleak"

	"github.com/vnovick/itervox/internal/gitexec"
)

// TestMain fails the package's test run when a goroutine is still running
// after every test finished (CORE-112). The ignore list is empty: an
// exemption must name one exact top function (goleak.IgnoreTopFunction) with
// a one-line lifecycle reason — no package-wide or prefix exemptions.
//
// It first drops GIT_DIR and the other repository-location variables git
// exports into hooks (the pre-push hook runs `make test`), so no test git
// call can reach the developer's real repository. See internal/gitexec.
func TestMain(m *testing.M) {
	gitexec.UnsetInProcess()
	goleak.VerifyTestMain(m)
}
