//go:build !linux

package sysmon

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// gopsutilSource reads the process table on macOS and Windows through
// gopsutil (pure Go: sysctl/libproc on macOS, the Win32 process and IP
// helper APIs on Windows). Files are not available on either: macOS needs
// the Endpoint Security entitlement for file events, and Windows exposes
// another process's handles only to a privileged tool.
type gopsutilSource struct {
	argv map[procKey][]string
	// conns caches the machine's inet connections by pid, read at connsAt.
	conns   map[int32][]NetConn
	connsAt time.Time
}

// connsRefresh is how long a machine-wide connection read is reused. On
// macOS gopsutil reads connections by running lsof, which is too slow to
// run every sample; Windows asks the IP helper API, which is cheap.
func connsRefresh() time.Duration {
	if runtime.GOOS == "darwin" {
		return 10 * time.Second
	}
	return 0
}

func newProcessSource() processSource {
	return &gopsutilSource{argv: map[procKey][]string{}}
}

func (s *gopsutilSource) beginTick() {
	if time.Since(s.connsAt) >= connsRefresh() {
		s.conns = nil
	}
}

func (s *gopsutilSource) ioKnown() bool { return runtime.GOOS == "windows" }

func (s *gopsutilSource) list() ([]procInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ps, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]procInfo, 0, len(ps))
	live := map[procKey]bool{}
	for _, p := range ps {
		ppid, err := p.PpidWithContext(ctx)
		if err != nil {
			continue
		}
		created, _ := p.CreateTimeWithContext(ctx)
		info := procInfo{PID: int(p.Pid), PPID: int(ppid), PGID: processGroup(int(p.Pid)), Start: uint64(max(created, 0))}
		live[info.key()] = true
		out = append(out, info)
	}
	for k := range s.argv {
		if !live[k] {
			delete(s.argv, k)
		}
	}
	return out, nil
}

func (s *gopsutilSource) detail(p *procInfo, activity bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	proc, err := process.NewProcessWithContext(ctx, int32(p.PID))
	if err != nil {
		return
	}
	if name, err := proc.NameWithContext(ctx); err == nil {
		p.Comm = name
	}
	if argv, ok := s.argv[p.key()]; ok {
		p.Argv = argv
	} else if argv, err := proc.CmdlineSliceWithContext(ctx); err == nil {
		p.Argv = argv
		s.argv[p.key()] = argv
	}
	if t, err := proc.TimesWithContext(ctx); err == nil {
		p.CPUSeconds = t.User + t.System
	}
	if m, err := proc.MemoryInfoWithContext(ctx); err == nil {
		p.RSSBytes = m.RSS
	}
	if s.ioKnown() {
		if io, err := proc.IOCountersWithContext(ctx); err == nil {
			p.ReadBytes, p.WriteBytes, p.IOKnown = io.ReadBytes, io.WriteBytes, true
		}
	}
	p.Conns = s.connections(ctx, int32(p.PID))
}

// connections returns pid's inet sockets from one machine-wide read per
// tick (per connsRefresh on macOS).
func (s *gopsutilSource) connections(ctx context.Context, pid int32) []NetConn {
	if s.conns == nil {
		s.conns, s.connsAt = map[int32][]NetConn{}, time.Now()
		all, err := net.ConnectionsWithContext(ctx, "inet")
		if err != nil {
			return nil
		}
		for i, c := range all {
			proto := "tcp"
			if c.Type == 2 { // SOCK_DGRAM
				proto = "udp"
			}
			if c.Family == 23 || c.Family == 30 || strings.Contains(c.Laddr.IP, ":") { // AF_INET6 (Windows, macOS)
				proto += "6"
			}
			nc := NetConn{Protocol: proto, Local: joinHostPort(c.Laddr.IP, c.Laddr.Port), State: strings.ToUpper(c.Status),
				Inode: uint64(i + 1)}
			if c.Raddr.Port != 0 {
				nc.Remote = joinHostPort(c.Raddr.IP, c.Raddr.Port)
			}
			if nc.State == "" || nc.State == "NONE" {
				nc.State = "UNCONN"
				if nc.Remote != "" {
					nc.State = "ESTABLISHED"
				}
			}
			s.conns[c.Pid] = append(s.conns[c.Pid], nc)
		}
	}
	return s.conns[pid]
}

func joinHostPort(ip string, port uint32) string {
	if strings.Contains(ip, ":") {
		return "[" + ip + "]:" + strconv.Itoa(int(port))
	}
	return ip + ":" + strconv.Itoa(int(port))
}
