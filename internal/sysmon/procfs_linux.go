//go:build linux

package sysmon

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// maxFDsScanned bounds how many fds of one process a sample inspects.
const maxFDsScanned = 512

// procfsSource reads /proc (root is "/proc" in production and a fixture
// tree in tests).
type procfsSource struct {
	root     string
	pageSize uint64
	// nets caches the inet tables per network namespace for one tick.
	nets map[string]map[uint64]NetConn
	// argv caches each process's command line: it does not change after
	// exec, and the sampler sees each member every tick.
	argv map[procKey][]string
}

func newProcessSource() processSource {
	return newProcfsSource("/proc")
}

func newProcfsSource(root string) *procfsSource {
	return &procfsSource{root: root, pageSize: uint64(os.Getpagesize()), argv: map[procKey][]string{}}
}

func (s *procfsSource) beginTick() { s.nets = map[string]map[uint64]NetConn{} }

func (s *procfsSource) ioKnown() bool { return true }

func (s *procfsSource) list() ([]procInfo, error) {
	ents, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	out := make([]procInfo, 0, len(ents))
	live := make(map[procKey]bool, len(ents))
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.root, e.Name(), "stat"))
		if err != nil {
			continue // exited between ReadDir and here
		}
		st, err := parseStat(b)
		if err != nil {
			continue
		}
		p := procInfo{PID: pid, PPID: st.PPID, PGID: st.PGID, Start: st.StartTicks, Comm: st.Comm,
			CPUSeconds: float64(st.CPUTicks) / clockTicks, RSSBytes: st.RSSPages * s.pageSize}
		live[p.key()] = true
		out = append(out, p)
	}
	for k := range s.argv {
		if !live[k] {
			delete(s.argv, k)
		}
	}
	return out, nil
}

func (s *procfsSource) detail(p *procInfo, activity bool) {
	dir := filepath.Join(s.root, strconv.Itoa(p.PID))
	if argv, ok := s.argv[p.key()]; ok {
		p.Argv = argv
	} else if b, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		p.Argv = parseCmdline(b)
		s.argv[p.key()] = p.Argv
	}
	if f, err := os.Open(filepath.Join(dir, "io")); err == nil {
		p.ReadBytes, p.WriteBytes, p.IOKnown = parseIO(f)
		_ = f.Close()
	}
	// Sockets are read always (the connection count is a resource figure);
	// regular files only when activity is recorded.
	fds, err := os.ReadDir(filepath.Join(dir, "fd"))
	if err != nil {
		return
	}
	var inodes []uint64
	for i, fd := range fds {
		if i == maxFDsScanned {
			break
		}
		link, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
		if err != nil {
			continue
		}
		if ino, ok := socketInode(link); ok {
			inodes = append(inodes, ino)
			continue
		}
		path, ok := regularFilePath(link)
		if !ok || !activity {
			continue
		}
		info, _ := os.ReadFile(filepath.Join(dir, "fdinfo", fd.Name()))
		p.Files = append(p.Files, openFile{Path: path, Write: parseFdinfoWrite(info)})
	}
	if len(inodes) == 0 {
		return
	}
	table := s.netTable(dir)
	for _, ino := range inodes {
		if c, ok := table[ino]; ok {
			p.Conns = append(p.Conns, c)
		}
	}
}

// regularFilePath keeps fd targets that name a file on disk: absolute
// paths outside /dev, /proc and /sys, with a " (deleted)" suffix dropped.
func regularFilePath(link string) (string, bool) {
	if !strings.HasPrefix(link, "/") {
		return "", false // pipe:[..], anon_inode:[..], socket:[..]
	}
	return regularPath(strings.TrimSuffix(link, " (deleted)"))
}

// netTable returns the inet sockets of the network namespace pidDir's
// process lives in, read once per tick per namespace (an isolated run may
// have its own).
func (s *procfsSource) netTable(pidDir string) map[uint64]NetConn {
	ns, err := os.Readlink(filepath.Join(pidDir, "ns", "net"))
	if err != nil {
		ns = "unknown"
	}
	if t, ok := s.nets[ns]; ok {
		return t
	}
	t := map[uint64]NetConn{}
	for _, proto := range []string{"tcp", "tcp6", "udp", "udp6"} {
		b, err := os.ReadFile(filepath.Join(pidDir, "net", proto))
		if err != nil {
			continue
		}
		for _, c := range parseNetTable(bytes.NewReader(b), proto) {
			if c.Inode != 0 {
				t[c.Inode] = c
			}
		}
	}
	if s.nets != nil {
		s.nets[ns] = t
	}
	return t
}
