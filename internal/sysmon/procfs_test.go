package sysmon

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "proc", name))
	require.NoError(t, err)
	return b
}

// TestParseStatHandlesParensInComm: comm is cut at the LAST ")", so a
// command name with spaces and parentheses does not shift the fields.
func TestParseStatHandlesParensInComm(t *testing.T) {
	st, err := parseStat(fixture(t, "stat_parens"))
	require.NoError(t, err)
	assert.Equal(t, statFields{Comm: "node (worker) x", PPID: 4241, PGID: 4200, CPUTicks: 325, StartTicks: 98765, RSSPages: 12345}, st)

	_, err = parseStat([]byte("12 (short) S 1 2"))
	assert.Error(t, err)
	_, err = parseStat([]byte("no parens at all"))
	assert.Error(t, err)
}

func TestParseCmdlineIOAndFdinfo(t *testing.T) {
	assert.Equal(t, []string{"claude", "--print", "--output-format", "stream-json"}, parseCmdline(fixture(t, "cmdline")))
	assert.Nil(t, parseCmdline([]byte("\x00")))

	r, w, ok := parseIO(bytes.NewReader(fixture(t, "io")))
	assert.True(t, ok)
	assert.Equal(t, uint64(4096), r)
	assert.Equal(t, uint64(81920), w)
	_, _, ok = parseIO(bytes.NewReader([]byte("rchar: 1\n")))
	assert.False(t, ok, "a kernel without per-task I/O accounting reports no bytes")

	assert.True(t, parseFdinfoWrite(fixture(t, "fdinfo_write")), "O_WRONLY")
	assert.True(t, parseFdinfoWrite(fixture(t, "fdinfo_rdwr")), "O_RDWR (with O_CLOEXEC)")
	assert.False(t, parseFdinfoWrite(fixture(t, "fdinfo_read")), "O_RDONLY")

	ino, ok := socketInode("socket:[31002]")
	assert.True(t, ok)
	assert.Equal(t, uint64(31002), ino)
	_, ok = socketInode("pipe:[9]")
	assert.False(t, ok)
}

// TestParseNetTables decodes the kernel's little-endian hex addresses
// (IPv4, IPv6 and v4-mapped IPv6) and the TCP/UDP states.
func TestParseNetTables(t *testing.T) {
	tcp := parseNetTable(bytes.NewReader(fixture(t, "net_tcp")), "tcp")
	assert.Equal(t, []NetConn{
		{Protocol: "tcp", Local: "127.0.0.1:8080", State: "LISTEN", Inode: 31001},
		{Protocol: "tcp", Local: "10.0.2.15:50000", Remote: "93.184.216.34:443", State: "ESTABLISHED", Inode: 31002},
	}, tcp, "the malformed line is skipped")
	assert.True(t, tcp[0].Listening())
	assert.False(t, tcp[1].Listening())

	tcp6 := parseNetTable(bytes.NewReader(fixture(t, "net_tcp6")), "tcp6")
	assert.Equal(t, []NetConn{
		{Protocol: "tcp6", Local: "[::1]:40000", Remote: "[::1]:443", State: "ESTABLISHED", Inode: 31003},
		{Protocol: "tcp6", Local: "127.0.0.1:40960", Remote: "12.11.10.37:80", State: "TIME_WAIT", Inode: 0},
	}, tcp6)

	udp := parseNetTable(bytes.NewReader(fixture(t, "net_udp")), "udp")
	assert.Equal(t, []NetConn{
		{Protocol: "udp", Local: "0.0.0.0:5353", State: "UNCONN", Inode: 32001},
		{Protocol: "udp", Local: "10.0.2.15:54321", Remote: "8.8.8.8:53", State: "ESTABLISHED", Inode: 32002},
	}, udp)
}

func TestParseMachineFiles(t *testing.T) {
	l1, l5, l15, ok := parseLoadavg(fixture(t, "loadavg"))
	assert.True(t, ok)
	assert.Equal(t, []float64{0.52, 1.25, 2.50}, []float64{l1, l5, l15})

	total, avail, ok := parseMeminfo(bytes.NewReader(fixture(t, "meminfo")))
	assert.True(t, ok)
	assert.Equal(t, uint64(16384000*1024), total)
	assert.Equal(t, uint64(2048000*1024), avail)

	psi, ok := parsePSISomeAvg10(fixture(t, "pressure_memory"))
	assert.True(t, ok)
	assert.InDelta(t, 7.5, psi, 1e-9)

	assert.Equal(t, PressureModerate, memPressureLevel(total, avail, psi, true))
	assert.Equal(t, PressureHigh, memPressureLevel(total, avail, 30, true))
	assert.Equal(t, PressureOK, memPressureLevel(total, avail, 1, true))
	// Without PSI the available share decides: 2/16 = 12.5% is moderate.
	assert.Equal(t, PressureModerate, memPressureLevel(total, avail, 0, false))
	assert.Equal(t, PressureHigh, memPressureLevel(100, 4, 0, false))
	assert.Equal(t, PressureOK, memPressureLevel(100, 50, 0, false))
}

func TestApplyDiskThresholds(t *testing.T) {
	st := MachineStatus{Disk: &DiskStatus{FreeBytes: 3 << 30, TotalBytes: 100 << 30}}
	applyDiskThresholds(&st, 5<<30, 0)
	assert.True(t, st.DiskLow)
	assert.False(t, st.DiskCritical)

	applyDiskThresholds(&st, 5<<30, 4<<30)
	assert.True(t, st.DiskLow)
	assert.True(t, st.DiskCritical)

	applyDiskThresholds(&st, 0, 0)
	assert.False(t, st.DiskLow, "0 turns the warning off")

	applyDiskThresholds(&st, 2<<30, 0)
	assert.False(t, st.DiskLow, "3 GB free is above a 2 GB threshold")

	none := MachineStatus{}
	applyDiskThresholds(&none, 5<<30, 1<<30)
	assert.False(t, none.DiskLow, "an unread disk never warns")
}
