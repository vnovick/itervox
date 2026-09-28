package main

import "github.com/vnovick/itervox/internal/gitexec"

// init (not TestMain: this package's re-exec child fixtures hook in via init
// and must not depend on a TestMain) drops GIT_DIR and the other
// repository-location variables that git exports into hooks — the pre-push
// hook runs `make test` — so no test git call, in this binary or a re-exec'd
// child, can reach the developer's real repository. See internal/gitexec.
func init() {
	gitexec.UnsetInProcess()
}
