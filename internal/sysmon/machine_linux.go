//go:build linux

package sysmon

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// readMachine reads the load averages, memory and PSI from procRoot and the
// free space of diskPath's volume.
func readMachine(procRoot, diskPath string, now time.Time) MachineStatus {
	st := MachineStatus{CPUCount: runtime.NumCPU(), MemPressure: PressureOK, SampledAt: now}
	if b, err := os.ReadFile(filepath.Join(procRoot, "loadavg")); err == nil {
		st.Load1, st.Load5, st.Load15, st.LoadKnown = parseLoadavg(b)
	}
	if b, err := os.ReadFile(filepath.Join(procRoot, "meminfo")); err == nil {
		total, avail, ok := parseMeminfo(bytes.NewReader(b))
		if ok {
			st.MemTotalBytes, st.MemAvailableBytes = total, avail
		}
		psi, psiOK := 0.0, false
		if p, err := os.ReadFile(filepath.Join(procRoot, "pressure", "memory")); err == nil {
			psi, psiOK = parsePSISomeAvg10(p)
		}
		st.MemPressure = memPressureLevel(total, avail, psi, psiOK)
	}
	st.Disk = diskStatus(diskPath)
	return st
}

func diskStatus(path string) *DiskStatus {
	if path == "" {
		return nil
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(existingAncestor(path), &fs); err != nil {
		return nil
	}
	bsize := uint64(fs.Bsize) //nolint:gosec // block size is positive
	return &DiskStatus{Path: path, FreeBytes: fs.Bavail * bsize, TotalBytes: fs.Blocks * bsize}
}
