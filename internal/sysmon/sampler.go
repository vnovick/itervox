package sysmon

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/logging"
)

// procInfo is one process as a processSource reports it. list fills the
// identity fields (and CPU/RSS where that is cheap); detail fills the rest
// for the processes that belong to a run.
type procInfo struct {
	PID, PPID, PGID int
	// Start is the process's start (Linux: ticks after boot; elsewhere its
	// creation time in ms). With PID it identifies the process across
	// samples despite pid reuse.
	Start      uint64
	Comm       string
	Argv       []string
	CPUSeconds float64
	RSSBytes   uint64
	ReadBytes  uint64
	WriteBytes uint64
	IOKnown    bool
	// Files are the regular files the process has open (filled only when
	// activity is recorded); Conns its inet sockets (always).
	Files []openFile
	Conns []NetConn
}

type openFile struct {
	Path  string
	Write bool
}

type procKey struct {
	pid   int
	start uint64
}

func (p *procInfo) key() procKey { return procKey{p.PID, p.Start} }

// processSource reads the OS process table (procfs_linux.go,
// process_gopsutil.go).
type processSource interface {
	// beginTick drops per-tick caches (the Linux net tables).
	beginTick()
	// list returns every process with its identity fields.
	list() ([]procInfo, error)
	// detail fills a run member's remaining fields, its connections
	// included; open files only when activity is true. Errors (a process
	// that exited, a file that cannot be read) leave the fields empty.
	detail(p *procInfo, activity bool)
	// ioKnown reports whether the OS reports per-process disk I/O.
	ioKnown() bool
}

// memberIndexes returns the indexes of the processes that belong to a run:
// members of its process groups, its group leaders, and every descendant of
// those (a descendant that started its own group or session still counts).
// pgid <= 0 never matches: Windows has no process groups.
func memberIndexes(procs []procInfo, pgids, leaders map[int]bool) []int {
	in := make(map[int]bool, 8)
	children := make(map[int][]int, len(procs))
	for i := range procs {
		p := &procs[i]
		children[p.PPID] = append(children[p.PPID], i)
		if (p.PGID > 0 && pgids[p.PGID]) || leaders[p.PID] {
			in[i] = true
		}
	}
	queue := make([]int, 0, len(in))
	for i := range in {
		queue = append(queue, i)
	}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		for _, c := range children[procs[i].PID] {
			if !in[c] && procs[c].PID != procs[i].PID {
				in[c] = true
				queue = append(queue, c)
			}
		}
	}
	out := make([]int, 0, len(in))
	for i := range in {
		out = append(out, i)
	}
	slices.Sort(out)
	return out
}

// procMark is what the sampler remembers of a live member between samples.
type procMark struct {
	cpu         float64
	read, write uint64
}

// runSampler turns successive member lists into a RunUsage. Owned by the
// sampler goroutine.
type runSampler struct {
	prev        map[procKey]procMark
	exitedCPU   float64
	exitedRead  uint64
	exitedWrite uint64
	lastTotal   float64
	lastAt      time.Time
}

func newRunSampler(start time.Time) *runSampler {
	return &runSampler{prev: map[procKey]procMark{}, lastAt: start}
}

// topCommandsShown bounds RunUsage.TopCommands.
const topCommandsShown = 5

// argsMaxBytes bounds a child command's redacted argument string.
const argsMaxBytes = 160

// observe folds one sample of the run's members into u. Totals are
// monotonic: a process that exited keeps the CPU and I/O it was last seen
// with (work between its last sample and its exit is not counted).
func (s *runSampler) observe(now time.Time, ms []procInfo, ioKnown bool, u *RunUsage) {
	seen := make(map[procKey]bool, len(ms))
	var liveCPU float64
	var liveRead, liveWrite, rss uint64
	dt := now.Sub(s.lastAt).Seconds()
	top := make([]ChildCommand, 0, len(ms))
	conns := map[uint64]bool{}
	for i := range ms {
		p := &ms[i]
		k := p.key()
		seen[k] = true
		liveCPU += p.CPUSeconds
		liveRead += p.ReadBytes
		liveWrite += p.WriteBytes
		rss += p.RSSBytes
		prev, known := s.prev[k]
		delta := p.CPUSeconds
		if known {
			delta = p.CPUSeconds - prev.cpu
		}
		pct := 0.0
		if dt > 0 {
			pct = max(delta, 0) / dt * 100
		}
		name, args := commandLine(p)
		top = append(top, ChildCommand{PID: p.PID, Name: name, Args: args, CPUPercent: round1(pct), RSSBytes: p.RSSBytes})
		s.prev[k] = procMark{cpu: p.CPUSeconds, read: p.ReadBytes, write: p.WriteBytes}
		for _, c := range p.Conns {
			if !c.Listening() {
				conns[c.Inode] = true
			}
		}
	}
	for k, m := range s.prev {
		if !seen[k] {
			s.exitedCPU += m.cpu
			s.exitedRead += m.read
			s.exitedWrite += m.write
			delete(s.prev, k)
		}
	}
	total := s.exitedCPU + liveCPU
	if dt > 0 {
		u.CPUPercent = round1(max(total-s.lastTotal, 0) / dt * 100)
	}
	s.lastTotal, s.lastAt = total, now
	u.CPUSeconds = round1(total)
	u.RSSBytes = rss
	u.PeakRSSBytes = max(u.PeakRSSBytes, rss)
	u.Processes = len(ms)
	u.PeakProcesses = max(u.PeakProcesses, len(ms))
	u.DiskIOKnown = ioKnown
	u.DiskReadBytes = s.exitedRead + liveRead
	u.DiskWriteBytes = s.exitedWrite + liveWrite
	u.NetConnections = len(conns)
	slices.SortFunc(top, func(a, b ChildCommand) int {
		return cmp.Or(cmp.Compare(b.CPUPercent, a.CPUPercent), cmp.Compare(b.RSSBytes, a.RSSBytes), cmp.Compare(a.PID, b.PID))
	})
	u.TopCommands = top[:min(len(top), topCommandsShown)]
	u.Samples++
	u.SampledAt = now
}

// commandLine returns a process's display name (the base name of argv[0],
// else its comm) and its arguments, redacted, whitespace-collapsed and cut
// to argsMaxBytes. Redaction runs before the cut so a cut secret is still
// caught.
func commandLine(p *procInfo) (name, args string) {
	name = p.Comm
	if len(p.Argv) > 0 {
		if b := filepath.Base(p.Argv[0]); b != "" && b != "." {
			name = b
		}
	}
	name = boundUTF8(logging.RedactString(name), 64)
	if len(p.Argv) > 1 {
		args = boundUTF8(strings.Join(strings.Fields(logging.RedactString(strings.Join(scrubArgs(p.Argv[1:]), " "))), " "), argsMaxBytes)
	}
	return name, args
}

// redactedArg replaces a scrubbed argument value.
const redactedArg = "[redacted]"

// secretFlagWords mark a flag whose value is a credential.
var secretFlagWords = []string{"token", "secret", "password", "passwd", "apikey", "api-key", "api_key", "auth", "credential", "private-key"}

func secretFlag(flag string) bool {
	f := strings.ToLower(strings.TrimLeft(flag, "-"))
	for _, w := range secretFlagWords {
		if strings.Contains(f, w) {
			return true
		}
	}
	return false
}

// scrubArgs redacts what logging.RedactString cannot recognise by shape:
// the values of environment assignments (NAME=value) and of flags named
// like credentials (--token=x, --password x), and URL user info.
func scrubArgs(argv []string) []string {
	out := make([]string, len(argv))
	redactNext := false
	for i, a := range argv {
		switch {
		case redactNext:
			out[i], redactNext = redactedArg, false
			continue
		case strings.HasPrefix(a, "-"):
			flag, _, hasValue := strings.Cut(a, "=")
			if secretFlag(flag) {
				if hasValue {
					out[i] = flag + "=" + redactedArg
				} else {
					out[i], redactNext = a, true
				}
				continue
			}
		case isEnvAssignment(a):
			name, _, _ := strings.Cut(a, "=")
			out[i] = name + "=" + redactedArg
			continue
		}
		out[i] = scrubURLUser(a)
	}
	return out
}

// isEnvAssignment reports whether a looks like NAME=value with a shell
// variable name.
func isEnvAssignment(a string) bool {
	name, _, ok := strings.Cut(a, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// scrubURLUser drops the user info of a scheme://user:pass@host argument.
func scrubURLUser(a string) string {
	scheme, rest, ok := strings.Cut(a, "://")
	if !ok {
		return a
	}
	at := strings.IndexByte(rest, '@')
	slash := strings.IndexByte(rest, '/')
	if at < 0 || (slash >= 0 && slash < at) {
		return a
	}
	return scheme + "://" + redactedArg + rest[at:]
}

// boundUTF8 cuts s to at most n bytes on a rune boundary, marking a cut
// with an ellipsis.
func boundUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:max(cut, 0)] + "…"
}

func round1(f float64) float64 {
	return float64(int64(f*10+0.5)) / 10
}
