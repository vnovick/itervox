package main

import (
	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/workflow"
)

// writeInitWorkflow writes the generated WORKFLOW.md for `itervox init`
// (including `--force`, which overwrites an existing one) under
// workflow.WithEditLock, the same in-process + inter-process lock every
// other WORKFLOW.md writer takes (CORE-149). Without it, `init --force`
// could land in the middle of a running daemon's settings read-modify-write
// or an `itervox init --update` migration and be silently overwritten by it,
// or overwrite it (V5). The write is not registered as a self-write, so a
// daemon watching the file reloads onto the new content.
func writeInitWorkflow(path string, content []byte) error {
	return workflow.WithEditLock(path, func() error {
		return atomicfs.WriteFile(path, content, 0o644)
	})
}
