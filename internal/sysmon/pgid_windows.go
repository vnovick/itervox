//go:build windows

package sysmon

// processGroup is 0 on Windows, which has no process groups: a run's
// members are its leaders' descendants (procgroup puts them in a Job
// Object, which the process table does not expose).
func processGroup(int) int { return 0 }
