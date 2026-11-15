// Package sysmon is the daemon's system visibility: what each agent run
// costs the machine (CPU, memory, processes, disk I/O, network connections)
// and what it did (commands started, files written or touched, connections
// opened), plus the machine's own load, memory pressure and free disk.
//
// # Where the data comes from
//
// A Monitor is a procgroup.Observer (installed next to the CORE-042 ledger),
// so it learns about every LOCAL process group an agent run or hook starts,
// labelled with the issue identifier. The orchestrator's worker names the run
// (AttachRun: run ID and workspace) and its event loop closes it when the run
// is recorded in history (EndRun). Between those calls one sampler goroutine
// reads the run's process tree at most every SampleInterval:
//
//   - Linux: /proc directly (stat, io, cmdline, fd, fdinfo and the net
//     tables), with no dependency. The parsers are pure and fixture-tested.
//   - macOS and Windows: gopsutil (pure Go, no cgo) for processes, memory,
//     CPU times, I/O counters (Windows) and connections.
//
// Activity is recorded per run, bounded and deduplicated, in one of three
// modes: off, sampled (the default: new processes, open files and sockets
// seen by the sampler, so short-lived activity between two samples can be
// missed) and precise (Linux only: an eBPF program on the exec, openat and
// connect tracepoints scoped to the run's process tree; it needs CAP_BPF +
// CAP_PERFMON or root and a mounted tracefs, and falls back to sampled with
// the reason when any of that is missing). File activity is not available on
// macOS (it needs the Endpoint Security entitlement) or Windows.
//
// # Concurrency
//
// None of this is orchestrator State. The Monitor is a separately locked
// store, like the comment counters: the sampler and the eBPF reader write it
// under Monitor.mu, the snapshot builder, the run-context and activity
// endpoints and the metrics view read copies under the same lock, and the
// event loop's only calls (EndRun) take the lock briefly and never do I/O.
// Sampling, file reads and activity-log writes happen on the sampler
// goroutine with the lock released.
//
// Activity logs are runtime state under .itervox/runtime/activity
// (gitignored): one JSON document per run plus an index, rotated by count and
// total size. Command lines and paths pass through logging.RedactString
// before they are stored or served.
package sysmon
