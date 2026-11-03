package sysmon

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"
)

// Parsers for Linux /proc files. They are pure (bytes in, values out) so the
// fixtures under testdata/proc exercise them on every OS; procfs_linux.go
// reads the files.

// clockTicks is USER_HZ, the unit of utime/stime in /proc/<pid>/stat. It is
// part of the userspace ABI and 100 on every architecture Linux supports
// today, independent of the kernel's internal HZ.
const clockTicks = 100

// statFields is what parseStat extracts from /proc/<pid>/stat.
type statFields struct {
	Comm       string
	PPID       int
	PGID       int
	CPUTicks   uint64 // utime + stime
	StartTicks uint64 // starttime, ticks after boot (process identity with the pid)
	RSSPages   uint64
}

var errShortStat = errors.New("sysmon: short /proc stat line")

// parseStat parses one /proc/<pid>/stat line. comm is the text between the
// first "(" and the LAST ")", since a command name may itself contain
// spaces or parentheses.
func parseStat(b []byte) (statFields, error) {
	s := string(b)
	open := strings.IndexByte(s, '(')
	closeIdx := strings.LastIndexByte(s, ')')
	if open < 0 || closeIdx < open {
		return statFields{}, errShortStat
	}
	f := strings.Fields(s[closeIdx+1:])
	// f[0] is the state (field 3); field n is f[n-3].
	if len(f) < 22 {
		return statFields{}, errShortStat
	}
	var out statFields
	out.Comm = s[open+1 : closeIdx]
	var err error
	if out.PPID, err = strconv.Atoi(f[1]); err != nil {
		return statFields{}, fmt.Errorf("sysmon: stat ppid: %w", err)
	}
	if out.PGID, err = strconv.Atoi(f[2]); err != nil {
		return statFields{}, fmt.Errorf("sysmon: stat pgrp: %w", err)
	}
	utime, err1 := strconv.ParseUint(f[11], 10, 64)
	stime, err2 := strconv.ParseUint(f[12], 10, 64)
	start, err3 := strconv.ParseUint(f[19], 10, 64)
	rss, err4 := strconv.ParseInt(f[21], 10, 64)
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return statFields{}, fmt.Errorf("sysmon: stat counters: %w", err)
	}
	out.CPUTicks = utime + stime
	out.StartTicks = start
	out.RSSPages = uint64(max(rss, 0))
	return out, nil
}

// parseCmdline splits /proc/<pid>/cmdline (NUL-separated, maybe
// NUL-terminated) into argv.
func parseCmdline(b []byte) []string {
	s := strings.TrimRight(string(b), "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// parseIO reads read_bytes and write_bytes from /proc/<pid>/io.
func parseIO(r io.Reader) (read, write uint64, ok bool) {
	sc := bufio.NewScanner(r)
	var seenR, seenW bool
	for sc.Scan() {
		k, v, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "read_bytes":
			read, seenR = n, true
		case "write_bytes":
			write, seenW = n, true
		}
	}
	return read, write, seenR && seenW
}

// Open flags (octal in /proc/<pid>/fdinfo/<fd>).
const (
	oAccMode = 0o3
	oWronly  = 0o1
	oRdwr    = 0o2
)

// parseFdinfoWrite reports whether /proc/<pid>/fdinfo/<fd> says the file is
// open for writing (O_WRONLY or O_RDWR).
func parseFdinfoWrite(b []byte) bool {
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(k) != "flags" {
			continue
		}
		flags, err := strconv.ParseUint(strings.TrimSpace(v), 8, 64)
		if err != nil {
			return false
		}
		acc := flags & oAccMode
		return acc == oWronly || acc == oRdwr
	}
	return false
}

// socketInode returns the inode of an fd link "socket:[12345]".
func socketInode(link string) (uint64, bool) {
	rest, ok := strings.CutPrefix(link, "socket:[")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSuffix(rest, "]"), 10, 64)
	return n, err == nil
}

// NetConn is one inet socket from the kernel's tables.
type NetConn struct {
	Protocol string // tcp, tcp6, udp, udp6
	Local    string // host:port
	Remote   string // host:port; empty for an unconnected socket
	State    string // ESTABLISHED, LISTEN, ... (UDP: ESTABLISHED or UNCONN)
	Inode    uint64
}

// Listening reports whether the socket only waits for connections.
func (c NetConn) Listening() bool { return c.State == "LISTEN" || c.State == "UNCONN" }

var tcpStates = map[string]string{
	"01": "ESTABLISHED", "02": "SYN_SENT", "03": "SYN_RECV", "04": "FIN_WAIT1",
	"05": "FIN_WAIT2", "06": "TIME_WAIT", "07": "CLOSE", "08": "CLOSE_WAIT",
	"09": "LAST_ACK", "0A": "LISTEN", "0B": "CLOSING",
}

// parseNetTable parses /proc/net/{tcp,tcp6,udp,udp6}; proto names the
// table. Malformed lines are skipped.
func parseNetTable(r io.Reader, proto string) []NetConn {
	var out []NetConn
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first { // the header
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		local, ok1 := parseHexAddr(f[1])
		remote, ok2 := parseHexAddr(f[2])
		inode, err := strconv.ParseUint(f[9], 10, 64)
		if !ok1 || !ok2 || err != nil {
			continue
		}
		c := NetConn{Protocol: proto, Local: local.String(), Inode: inode}
		if strings.HasPrefix(proto, "udp") {
			c.State = "UNCONN"
			if remote.Port() != 0 {
				c.State = "ESTABLISHED"
			}
		} else {
			c.State = tcpStates[strings.ToUpper(f[3])]
		}
		if remote.Port() != 0 {
			c.Remote = remote.String()
		}
		out = append(out, c)
	}
	return out
}

// parseHexAddr decodes "0100007F:1F90" (IPv4) or the 32-hex-digit IPv6
// form. The kernel prints each 32-bit word in host (little-endian on every
// architecture this daemon ships for) byte order.
func parseHexAddr(s string) (netip.AddrPort, bool) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	raw, err := hex.DecodeString(h)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, false
	}
	b := make([]byte, len(raw))
	for i := 0; i < len(raw); i += 4 {
		binary.BigEndian.PutUint32(b[i:], binary.LittleEndian.Uint32(raw[i:]))
	}
	addr, _ := netip.AddrFromSlice(b)
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), true
}

// parseLoadavg reads /proc/loadavg.
func parseLoadavg(b []byte) (l1, l5, l15 float64, ok bool) {
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return 0, 0, 0, false
	}
	var errs [3]error
	l1, errs[0] = strconv.ParseFloat(f[0], 64)
	l5, errs[1] = strconv.ParseFloat(f[1], 64)
	l15, errs[2] = strconv.ParseFloat(f[2], 64)
	return l1, l5, l15, errors.Join(errs[:]...) == nil
}

// parseMeminfo returns MemTotal and MemAvailable in bytes.
func parseMeminfo(r io.Reader) (total, avail uint64, ok bool) {
	sc := bufio.NewScanner(r)
	var seenT, seenA bool
	for sc.Scan() {
		k, v, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		switch k {
		case "MemTotal":
			total, seenT = n, true
		case "MemAvailable":
			avail, seenA = n, true
		}
	}
	return total, avail, seenT && seenA
}

// parsePSISomeAvg10 returns the "some avg10" percentage of
// /proc/pressure/memory: the share of the last 10s in which at least one
// task stalled on memory.
func parsePSISomeAvg10(b []byte) (float64, bool) {
	for _, line := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(line, "some ")
		if !ok {
			continue
		}
		for _, kv := range strings.Fields(rest) {
			if v, ok := strings.CutPrefix(kv, "avg10="); ok {
				f, err := strconv.ParseFloat(v, 64)
				return f, err == nil
			}
		}
	}
	return 0, false
}

// memPressureLevel classifies memory pressure: from PSI when known (share of
// time stalled), else from the share of memory still available.
func memPressureLevel(total, avail uint64, psi float64, psiKnown bool) string {
	if psiKnown {
		switch {
		case psi >= 25:
			return PressureHigh
		case psi >= 5:
			return PressureModerate
		}
		return PressureOK
	}
	if total == 0 {
		return PressureOK
	}
	share := float64(avail) / float64(total)
	switch {
	case share < 0.05:
		return PressureHigh
	case share < 0.15:
		return PressureModerate
	}
	return PressureOK
}
