//go:build !linux

package sysmon

import (
	"context"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// readMachine reads the load averages (none on Windows), memory and the
// free space of diskPath's volume through gopsutil. procRoot is unused.
func readMachine(_ string, diskPath string, now time.Time) MachineStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st := MachineStatus{CPUCount: runtime.NumCPU(), MemPressure: PressureOK, SampledAt: now}
	if runtime.GOOS != "windows" {
		if l, err := load.AvgWithContext(ctx); err == nil {
			st.Load1, st.Load5, st.Load15, st.LoadKnown = l.Load1, l.Load5, l.Load15, true
		}
	}
	if v, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		st.MemTotalBytes, st.MemAvailableBytes = v.Total, v.Available
		st.MemPressure = memPressureLevel(v.Total, v.Available, 0, false)
	}
	if diskPath != "" {
		if u, err := disk.UsageWithContext(ctx, existingAncestor(diskPath)); err == nil {
			st.Disk = &DiskStatus{Path: diskPath, FreeBytes: u.Free, TotalBytes: u.Total}
		}
	}
	return st
}
