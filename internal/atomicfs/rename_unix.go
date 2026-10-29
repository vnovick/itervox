//go:build unix

package atomicfs

import "os"

// renameReplace renames oldpath over newpath (rename(2) is atomic).
func renameReplace(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
