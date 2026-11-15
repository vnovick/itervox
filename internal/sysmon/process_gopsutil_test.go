//go:build !linux

package sysmon

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGopsutilSourceSeesThisProcess (macOS, Windows): the process table
// lists the test process with its parent, and detail fills its command
// line, CPU time and memory.
func TestGopsutilSourceSeesThisProcess(t *testing.T) {
	src := newProcessSource()
	src.beginTick()
	procs, err := src.list()
	require.NoError(t, err)
	i := slices.IndexFunc(procs, func(p procInfo) bool { return p.PID == os.Getpid() })
	require.GreaterOrEqual(t, i, 0, "the test process is listed")
	assert.Equal(t, os.Getppid(), procs[i].PPID)
	p := procs[i]
	src.detail(&p, true)
	assert.NotEmpty(t, p.Argv)
	assert.Positive(t, p.RSSBytes)
	assert.Empty(t, p.Files, "no file activity on this platform")

	st := readMachine("", t.TempDir(), time.Now())
	assert.Positive(t, st.MemTotalBytes)
	require.NotNil(t, st.Disk)
	assert.Positive(t, st.Disk.TotalBytes)
}
