//go:build linux

package sysmon

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParseTracepointFormat reads both sched_process_fork layouts (comm as
// char[16] on older kernels, __data_loc on newer ones) and a syscall one.
func TestParseTracepointFormat(t *testing.T) {
	newer := parseTracepointFormat(fixture(t, "format_fork_new"))
	assert.Equal(t, tpField{offset: 20, size: 4}, newer["child_pid"])
	assert.Equal(t, tpField{offset: 16, size: 4}, newer["child_comm"])
	older := parseTracepointFormat(fixture(t, "format_fork_old"))
	assert.Equal(t, tpField{offset: 44, size: 4}, older["child_pid"])
	assert.Equal(t, tpField{offset: 28, size: 16}, older["child_comm"])
	open := parseTracepointFormat(fixture(t, "format_openat"))
	assert.Equal(t, tpField{offset: 24, size: 8}, open["filename"])
	assert.Equal(t, tpField{offset: 32, size: 8}, open["flags"])
	assert.Equal(t, tpField{offset: 4, size: 4}, open["common_pid"])
}

func TestDecodePreciseEvent(t *testing.T) {
	rec := func(kind, pid, slot uint32, data []byte) []byte {
		b := make([]byte, bpfEventSize)
		binary.NativeEndian.PutUint32(b[0:], kind)
		binary.NativeEndian.PutUint32(b[4:], pid)
		binary.NativeEndian.PutUint32(b[8:], slot)
		copy(b[bpfDataOff:], data)
		return b
	}
	ev, ok := decodePreciseEvent(rec(preciseExec, 42, 3, []byte("/usr/bin/git\x00junk")))
	assert.True(t, ok)
	assert.Equal(t, preciseEvent{Kind: preciseExec, PID: 42, Slot: 3, Path: "/usr/bin/git"}, ev)

	v4 := make([]byte, 28)
	binary.NativeEndian.PutUint16(v4[0:], 2)
	binary.BigEndian.PutUint16(v4[2:], 443)
	copy(v4[4:], []byte{93, 184, 216, 34})
	ev, ok = decodePreciseEvent(rec(preciseConnect, 1, 1, v4))
	assert.True(t, ok)
	assert.Equal(t, "93.184.216.34:443", ev.Remote)
	assert.Equal(t, "inet", ev.Protocol)

	v6 := make([]byte, 28)
	binary.NativeEndian.PutUint16(v6[0:], 10)
	binary.BigEndian.PutUint16(v6[2:], 8443)
	v6[23] = 1 // ::1
	ev, ok = decodePreciseEvent(rec(preciseConnect, 1, 1, v6))
	assert.True(t, ok)
	assert.Equal(t, "[::1]:8443", ev.Remote)

	unix := make([]byte, 28)
	binary.NativeEndian.PutUint16(unix[0:], 1)
	_, ok = decodePreciseEvent(rec(preciseConnect, 1, 1, unix))
	assert.False(t, ok, "a unix-socket connect is not network activity")
	_, ok = decodePreciseEvent(rec(preciseOpen, 1, 1, nil))
	assert.False(t, ok, "an empty path")
	_, ok = decodePreciseEvent([]byte{1, 2})
	assert.False(t, ok, "a short record")
}
