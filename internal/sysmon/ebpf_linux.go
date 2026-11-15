//go:build linux

package sysmon

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
)

// Precise mode's eBPF side. The programs are assembled here with
// cilium/ebpf's asm package, so the repository carries no compiled BPF
// object and no clang step: each is a few dozen instructions.
//
//   - sched_process_fork: a child of a tracked process joins its slot.
//   - sched_process_exit: an exiting thread leaves the map.
//   - sched_process_exec, sys_enter_openat (O_WRONLY/O_RDWR/O_CREAT/O_TRUNC
//     only) and sys_enter_connect of a tracked process: one 272-byte event
//     into a ring buffer {kind, tgid, slot, aux, data[256]}.
//
// The "tracked" LRU hash (pid -> slot) is the scope: the run's group
// leaders are added from user space as they start, and the kernel adds
// every process they fork. Tracepoint field offsets are read from tracefs at
// load time, so the programs follow the running kernel's layout.

const (
	bpfEventSize = 272
	bpfDataOff   = 16
	bpfDataSize  = 256
	// openWriteMask is O_WRONLY|O_RDWR|O_CREAT|O_TRUNC.
	openWriteMask  = 0x1 | 0x2 | 0x40 | 0x200
	bpfLicense     = "Dual MIT/GPL"
	trackedEntries = 16384
	ringBytes      = 1 << 20
)

type ebpfTracer struct {
	tracked *ebpf.Map
	events  *ebpf.Map
	progs   []*ebpf.Program
	links   []link.Link
	reader  *ringbuf.Reader
	done    sync.WaitGroup
}

// openPreciseTracer loads and attaches the programs. Any failure (no
// CAP_BPF/CAP_PERFMON, no tracefs, a kernel without ring buffers) is
// returned for the fallback message, with everything opened so far closed.
func openPreciseTracer(deliver func(preciseEvent)) (t activityTracer, err error) {
	root, err := tracefsRoot()
	if err != nil {
		return nil, err
	}
	fExec, err := tracepointFields(root, "sched", "sched_process_exec", "filename")
	if err != nil {
		return nil, err
	}
	fFork, err := tracepointFields(root, "sched", "sched_process_fork", "child_pid")
	if err != nil {
		return nil, err
	}
	fOpen, err := tracepointFields(root, "syscalls", "sys_enter_openat", "filename", "flags")
	if err != nil {
		return nil, err
	}
	fConn, err := tracepointFields(root, "syscalls", "sys_enter_connect", "uservaddr", "addrlen")
	if err != nil {
		return nil, err
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("memlock limit: %w", err)
	}
	tr := &ebpfTracer{}
	defer func() {
		if err != nil {
			_ = tr.close()
		}
	}()
	if tr.tracked, err = ebpf.NewMap(&ebpf.MapSpec{Name: "itervox_tracked", Type: ebpf.LRUHash, KeySize: 4, ValueSize: 4, MaxEntries: trackedEntries}); err != nil {
		return nil, fmt.Errorf("create map: %w", err)
	}
	if tr.events, err = ebpf.NewMap(&ebpf.MapSpec{Name: "itervox_events", Type: ebpf.RingBuf, MaxEntries: ringBytes}); err != nil {
		return nil, fmt.Errorf("create ring buffer: %w", err)
	}
	type tp struct {
		group, name string
		insns       asm.Instructions
	}
	tracked, events := tr.tracked.FD(), tr.events.FD()
	for _, p := range []tp{
		{"sched", "sched_process_fork", forkProgram(tracked, fFork["child_pid"].offset)},
		{"sched", "sched_process_exit", exitProgram(tracked)},
		{"sched", "sched_process_exec", execProgram(tracked, events, fExec["filename"].offset)},
		{"syscalls", "sys_enter_openat", openProgram(tracked, events, fOpen["filename"].offset, fOpen["flags"].offset)},
		{"syscalls", "sys_enter_connect", connectProgram(tracked, events, fConn["uservaddr"].offset, fConn["addrlen"].offset)},
	} {
		prog, err := ebpf.NewProgram(&ebpf.ProgramSpec{Name: "itervox_" + strings.TrimPrefix(strings.TrimPrefix(p.name, "sched_process_"), "sys_enter_"),
			Type: ebpf.TracePoint, License: bpfLicense, Instructions: p.insns})
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", p.name, err)
		}
		tr.progs = append(tr.progs, prog)
		l, err := link.Tracepoint(p.group, p.name, prog, nil)
		if err != nil {
			return nil, fmt.Errorf("attach %s: %w", p.name, err)
		}
		tr.links = append(tr.links, l)
	}
	if tr.reader, err = ringbuf.NewReader(tr.events); err != nil {
		return nil, fmt.Errorf("ring buffer reader: %w", err)
	}
	tr.done.Add(1)
	go tr.read(deliver)
	return tr, nil
}

// prologue loads the current process's slot into R7 (exiting when it is
// not tracked) and keeps the context in R6 and the tgid at fp-4.
func prologue(tracked int) asm.Instructions {
	return asm.Instructions{
		asm.Mov.Reg(asm.R6, asm.R1),
		asm.FnGetCurrentPidTgid.Call(),
		asm.RSh.Imm(asm.R0, 32),
		asm.StoreMem(asm.RFP, -4, asm.R0, asm.Word),
		asm.LoadMapPtr(asm.R1, tracked),
		asm.Mov.Reg(asm.R2, asm.RFP),
		asm.Add.Imm(asm.R2, -4),
		asm.FnMapLookupElem.Call(),
		asm.JEq.Imm(asm.R0, 0, "exit"),
		asm.LoadMem(asm.R7, asm.R0, 0, asm.Word),
	}
}

// reserve reserves an event of kind in the ring buffer into R8 and writes
// its header; aux comes from R9 (when auxR9) or is 0.
func reserve(events int, kind int32, auxR9 bool) asm.Instructions {
	ins := asm.Instructions{
		asm.LoadMapPtr(asm.R1, events),
		asm.Mov.Imm(asm.R2, bpfEventSize),
		asm.Mov.Imm(asm.R3, 0),
		asm.FnRingbufReserve.Call(),
		asm.JEq.Imm(asm.R0, 0, "exit"),
		asm.Mov.Reg(asm.R8, asm.R0),
		asm.StoreImm(asm.R8, 0, int64(kind), asm.Word),
		asm.LoadMem(asm.R1, asm.RFP, -4, asm.Word),
		asm.StoreMem(asm.R8, 4, asm.R1, asm.Word),
		asm.StoreMem(asm.R8, 8, asm.R7, asm.Word),
	}
	if auxR9 {
		return append(ins, asm.StoreMem(asm.R8, 12, asm.R9, asm.Word))
	}
	return append(ins, asm.StoreImm(asm.R8, 12, 0, asm.Word))
}

// submitOrDiscard submits the event in R8 when the read helper before it
// returned > 0 (a string's length) or == 0 (probe_read_user), per okZero.
func submitOrDiscard(okZero bool) asm.Instructions {
	check := asm.JSLE.Imm(asm.R0, 0, "discard")
	if okZero {
		check = asm.JNE.Imm(asm.R0, 0, "discard")
	}
	return asm.Instructions{
		check,
		asm.Mov.Reg(asm.R1, asm.R8),
		asm.Mov.Imm(asm.R2, 0),
		asm.FnRingbufSubmit.Call(),
		asm.Ja.Label("exit"),
		asm.Mov.Reg(asm.R1, asm.R8).WithSymbol("discard"),
		asm.Mov.Imm(asm.R2, 0),
		asm.FnRingbufDiscard.Call(),
	}
}

func epilogue() asm.Instructions {
	return asm.Instructions{asm.Mov.Imm(asm.R0, 0).WithSymbol("exit"), asm.Return()}
}

func forkProgram(tracked, childOff int) asm.Instructions {
	ins := prologue(tracked)
	ins = append(ins,
		asm.StoreMem(asm.RFP, -8, asm.R7, asm.Word),
		asm.LoadMem(asm.R1, asm.R6, int16(childOff), asm.Word),
		asm.StoreMem(asm.RFP, -12, asm.R1, asm.Word),
		asm.LoadMapPtr(asm.R1, tracked),
		asm.Mov.Reg(asm.R2, asm.RFP),
		asm.Add.Imm(asm.R2, -12),
		asm.Mov.Reg(asm.R3, asm.RFP),
		asm.Add.Imm(asm.R3, -8),
		asm.Mov.Imm(asm.R4, 0),
		asm.FnMapUpdateElem.Call(),
	)
	return append(ins, epilogue()...)
}

func exitProgram(tracked int) asm.Instructions {
	ins := asm.Instructions{
		asm.FnGetCurrentPidTgid.Call(),
		asm.Mov.Reg32(asm.R0, asm.R0), // the thread id
		asm.StoreMem(asm.RFP, -4, asm.R0, asm.Word),
		asm.LoadMapPtr(asm.R1, tracked),
		asm.Mov.Reg(asm.R2, asm.RFP),
		asm.Add.Imm(asm.R2, -4),
		asm.FnMapDeleteElem.Call(),
	}
	return append(ins, epilogue()...)
}

func execProgram(tracked, events, filenameOff int) asm.Instructions {
	ins := prologue(tracked)
	ins = append(ins, reserve(events, preciseExec, false)...)
	ins = append(ins,
		// __data_loc: the low 16 bits are the string's offset in the record.
		asm.LoadMem(asm.R1, asm.R6, int16(filenameOff), asm.Word),
		asm.And.Imm(asm.R1, 0xffff),
		asm.Mov.Reg(asm.R3, asm.R6),
		asm.Add.Reg(asm.R3, asm.R1),
		asm.Mov.Reg(asm.R1, asm.R8),
		asm.Add.Imm(asm.R1, bpfDataOff),
		asm.Mov.Imm(asm.R2, bpfDataSize),
		asm.FnProbeReadKernelStr.Call(),
	)
	ins = append(ins, submitOrDiscard(false)...)
	return append(ins, epilogue()...)
}

func openProgram(tracked, events, filenameOff, flagsOff int) asm.Instructions {
	ins := prologue(tracked)
	ins = append(ins,
		asm.LoadMem(asm.R9, asm.R6, int16(flagsOff), asm.DWord),
		asm.Mov.Reg(asm.R1, asm.R9),
		asm.And.Imm(asm.R1, openWriteMask),
		asm.JEq.Imm(asm.R1, 0, "exit"),
	)
	ins = append(ins, reserve(events, preciseOpen, true)...)
	ins = append(ins,
		asm.LoadMem(asm.R3, asm.R6, int16(filenameOff), asm.DWord),
		asm.Mov.Reg(asm.R1, asm.R8),
		asm.Add.Imm(asm.R1, bpfDataOff),
		asm.Mov.Imm(asm.R2, bpfDataSize),
		asm.FnProbeReadUserStr.Call(),
	)
	ins = append(ins, submitOrDiscard(false)...)
	return append(ins, epilogue()...)
}

func connectProgram(tracked, events, addrOff, lenOff int) asm.Instructions {
	ins := prologue(tracked)
	ins = append(ins, asm.LoadMem(asm.R9, asm.R6, int16(lenOff), asm.DWord))
	ins = append(ins, reserve(events, preciseConnect, true)...)
	ins = append(ins,
		asm.LoadMem(asm.R3, asm.R6, int16(addrOff), asm.DWord),
		asm.Mov.Reg(asm.R1, asm.R8),
		asm.Add.Imm(asm.R1, bpfDataOff),
		asm.Mov.Imm(asm.R2, 28), // sizeof(struct sockaddr_in6)
		asm.FnProbeReadUser.Call(),
	)
	ins = append(ins, submitOrDiscard(true)...)
	return append(ins, epilogue()...)
}

// read delivers ring-buffer records until the reader is closed. A panic
// (in decoding or delivery) is logged with its stack and ends precise
// events; resource sampling is unaffected.
func (t *ebpfTracer) read(deliver func(preciseEvent)) {
	defer t.done.Done()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("sysmon: precise event reader panicked; precise events stop", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	var rec ringbuf.Record
	for {
		if err := t.reader.ReadInto(&rec); err != nil {
			if errors.Is(err, ringbuf.ErrClosed) {
				return
			}
			continue
		}
		if ev, ok := decodePreciseEvent(rec.RawSample); ok {
			deliver(ev)
		}
	}
}

// decodePreciseEvent decodes one ring-buffer record. Connects to anything
// but an IPv4/IPv6 address (a unix socket) are dropped.
func decodePreciseEvent(b []byte) (preciseEvent, bool) {
	if len(b) < bpfEventSize {
		return preciseEvent{}, false
	}
	ne := binary.NativeEndian
	ev := preciseEvent{Kind: int(ne.Uint32(b[0:])), PID: ne.Uint32(b[4:]), Slot: ne.Uint32(b[8:])}
	data := b[bpfDataOff:bpfEventSize]
	switch ev.Kind {
	case preciseExec, preciseOpen:
		if i := strings.IndexByte(string(data), 0); i >= 0 {
			data = data[:i]
		}
		ev.Path = string(data)
		return ev, ev.Path != ""
	case preciseConnect:
		port := binary.BigEndian.Uint16(data[2:4])
		switch ne.Uint16(data[0:2]) {
		case 2: // AF_INET
			ev.Protocol = "inet"
			ev.Remote = netip.AddrPortFrom(netip.AddrFrom4([4]byte(data[4:8])), port).String()
		case 10: // AF_INET6
			ev.Protocol = "inet6"
			ev.Remote = netip.AddrPortFrom(netip.AddrFrom16([16]byte(data[8:24])).Unmap(), port).String()
		default:
			return preciseEvent{}, false
		}
		return ev, true
	}
	return preciseEvent{}, false
}

func (t *ebpfTracer) track(pid, slot uint32) error {
	return t.tracked.Update(pid, slot, ebpf.UpdateAny)
}

func (t *ebpfTracer) untrack(slot uint32) {
	var k, v uint32
	var drop []uint32
	it := t.tracked.Iterate()
	for it.Next(&k, &v) {
		if v == slot {
			drop = append(drop, k)
		}
	}
	for _, k := range drop {
		_ = t.tracked.Delete(k)
	}
}

func (t *ebpfTracer) close() error {
	var errs []error
	for _, l := range t.links {
		errs = append(errs, l.Close())
	}
	if t.reader != nil {
		errs = append(errs, t.reader.Close())
		t.done.Wait()
	}
	for _, p := range t.progs {
		errs = append(errs, p.Close())
	}
	for _, m := range []*ebpf.Map{t.events, t.tracked} {
		if m != nil {
			errs = append(errs, m.Close())
		}
	}
	return errors.Join(errs...)
}
