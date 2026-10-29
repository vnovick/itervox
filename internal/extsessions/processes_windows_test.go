//go:build windows

package extsessions

import (
	"errors"
	"strings"
	"testing"
)

// TestSystemProcessesUnavailableOnWindows: process inspection (/proc, ps,
// lsof) does not exist on Windows; SystemProcesses says so instead of
// failing to run ps.
func TestSystemProcessesUnavailableOnWindows(t *testing.T) {
	procs, err := SystemProcesses()
	if !errors.Is(err, ErrProcessInspectionUnavailable) || procs != nil {
		t.Fatalf("SystemProcesses = %v, %v; want nil, ErrProcessInspectionUnavailable", procs, err)
	}
	if !strings.Contains(err.Error(), "not available on Windows") {
		t.Fatalf("error %q does not say it is unavailable on Windows", err)
	}
}
