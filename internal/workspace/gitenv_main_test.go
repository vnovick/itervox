package workspace_test

import (
	"testing"

	"github.com/vnovick/itervox/internal/gitexec"
)

// TestMain drops GIT_DIR, GIT_WORK_TREE and the other repository-location
// variables before any test runs. `git push` runs the lefthook pre-push hook,
// which runs `make test` with those variables exported by git; without this,
// a test git call that forgot gitexec would operate on the developer's real
// repository. Production code scrubs per call (gitexec.Command); this is the
// package-wide second layer. TestGitCommandsIgnoreInheritedGitDir re-sets
// them with t.Setenv to prove the per-call layer on its own.
func TestMain(m *testing.M) {
	gitexec.UnsetInProcess()
	m.Run()
}
