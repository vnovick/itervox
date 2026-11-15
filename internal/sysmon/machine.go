package sysmon

import (
	"os"
	"path/filepath"
)

// applyDiskThresholds sets DiskLow / DiskCritical from the free space and
// the configured thresholds (0 = that threshold is off).
func applyDiskThresholds(st *MachineStatus, warn, pause uint64) {
	st.DiskWarnBytes, st.DiskPauseBytes = warn, pause
	st.DiskLow, st.DiskCritical = false, false
	if st.Disk == nil || st.Disk.TotalBytes == 0 {
		return
	}
	free := st.Disk.FreeBytes
	st.DiskCritical = pause > 0 && free < pause
	st.DiskLow = st.DiskCritical || (warn > 0 && free < warn)
}

// existingAncestor returns path, or its nearest existing ancestor: the
// workspace root may not exist until the first workspace is created, and
// its volume is the one that will hold it.
func existingAncestor(path string) string {
	p := filepath.Clean(path)
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}
