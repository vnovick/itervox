// Package procgroup runs a subprocess in its own process group and kills the
// whole group on cancellation, re-sending SIGKILL until no member is left.
//
// It is the single shared home for this logic (CORE-150): both the agent
// runners (internal/agent) and the workspace hooks (internal/workspace) spawn
// shell pipelines whose children must die with them, and the package order
// forbids workspace importing agent. It imports only the standard library
// (plus golang.org/x/sys/windows on Windows) so it sits below every package
// that spawns subprocesses.
//
// On Windows (procgroup_windows.go) the same API is backed by a Job Object
// per child: Start places the child in a kill-on-close job before it runs,
// and Kill terminates the job, which ends every process in the tree.
package procgroup
