// Package atomicfs implements atomic file writes via the standard
// write-temp-then-rename dance: a write to `path` first writes to a temp file
// in the same directory, fsyncs, then renames into place. A SIGKILL or crash
// at any point leaves either the previous file content or the new content,
// never a partial write.
//
// Used for files that the daemon edits live and must remain readable to a
// concurrent reader (the watcher; the user via the dashboard) — WORKFLOW.md,
// the agent-actions store, the PID file, scaffold writers, etc.
package atomicfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// WriteFile atomically writes data to path with the given permission. The
// implementation is: create a temp file in the same directory (so the rename
// is on the same filesystem and is atomic), write the data, fsync to push
// bytes to disk, close, chmod to the requested permission, rename over the
// target, then fsync the parent directory so the rename itself is durable.
//
// If any step before rename fails, the temp file is removed and the original
// file is untouched. If the parent-dir fsync fails on platforms that don't
// support it (older macOS, some virtualised filesystems), the error is
// swallowed silently — the rename has already taken effect; only crash
// durability of the *directory entry* is in question, and the alternative
// (failing the write that already succeeded) is worse.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	// Dotfile prefix so file-watchers configured to ignore dotfiles don't
	// fire on the transient temp.
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("atomicfs: create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	// Best-effort cleanup if anything below fails.
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfs: write %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfs: fsync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomicfs: close %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("atomicfs: chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("atomicfs: rename %s -> %s: %w", tmpPath, path, err)
	}
	cleanup = false // rename succeeded; tmp no longer exists at tmpPath.

	// Best-effort: fsync the parent dir so the rename is durable across power
	// loss. Some filesystems return ENOTSUP / EINVAL on dir fsync; that's
	// acceptable here — the rename has already taken effect for any reader
	// in this process, and full crash-durability is a soft guarantee.
	if d, err := os.Open(dir); err == nil {
		_ = syncDir(d)
		_ = d.Close()
	}
	return nil
}

func syncDir(d *os.File) error {
	err := d.Sync()
	if err == nil {
		return nil
	}
	// Some filesystems / kernels return ENOTSUP or EINVAL on directory fsync.
	if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}
