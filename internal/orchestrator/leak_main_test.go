package orchestrator

import (
	"os"
	"testing"

	"go.uber.org/goleak"

	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/guardrails"
)

// TestMain fails the package's test run when a goroutine is still running
// after every test finished (CORE-112). The ignore list is empty: an
// exemption must name one exact top function (goleak.IgnoreTopFunction) with
// a one-line lifecycle reason — no package-wide or prefix exemptions.
//
// It first drops GIT_DIR and the other repository-location variables git
// exports into hooks (the pre-push hook runs `make test`), so no test git
// call can reach the developer's real repository. See internal/gitexec.
//
// The test binary also stands in for the itervox binary in guardrail shims
// (#204): a shim execs it with a guardrail entrypoint.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && guardrails.IsEntrypoint(os.Args[1]) {
		os.Exit(guardrails.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	gitexec.UnsetInProcess()
	goleak.VerifyTestMain(m)
}
