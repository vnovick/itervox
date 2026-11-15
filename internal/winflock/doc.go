// Package winflock is the Windows counterpart of the flock(2) calls the
// daemon's lock files use on Unix (the pid lock, the WORKFLOW.md edit lock,
// the API-token store lock and the hub lock). It has no code on other
// platforms: the Unix lock files keep calling syscall.Flock directly.
//
// LockFileEx locks, like flock locks, belong to one open handle and are
// released by the OS when the handle is closed or the process dies, so "is
// the lock free?" still answers "is the holder gone?". The lock covers one
// byte far beyond the end of the file rather than its content, because
// Windows byte-range locks are mandatory: locking the content would stop
// other processes from reading what the holder publishes there.
//
// It imports only the standard library and golang.org/x/sys/windows.
package winflock
