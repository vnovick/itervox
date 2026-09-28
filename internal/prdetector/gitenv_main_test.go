package prdetector_test

import (
	"testing"

	"github.com/vnovick/itervox/internal/gitexec"
)

// TestMain drops GIT_DIR and the other repository-location variables that
// git exports into hooks (the pre-push hook runs `make test`), so no test git
// call can reach the developer's real repository. See internal/gitexec.
func TestMain(m *testing.M) {
	gitexec.UnsetInProcess()
	m.Run()
}
