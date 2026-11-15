//go:build unix && !linux

package sysmon

import "syscall"

// processGroup returns pid's process group, or 0 when it cannot be read.
func processGroup(pid int) int {
	g, err := syscall.Getpgid(pid)
	if err != nil {
		return 0
	}
	return g
}
