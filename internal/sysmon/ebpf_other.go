//go:build !linux

package sysmon

import (
	"errors"
	"runtime"
)

// openPreciseTracer: precise mode is eBPF and Linux-only.
func openPreciseTracer(func(preciseEvent)) (activityTracer, error) {
	return nil, errors.New("precise mode needs Linux (eBPF); this is " + runtime.GOOS)
}
