package main

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// recordingFlusher stands in for *logbuffer.Buffer on run()'s exit path.
type recordingFlusher struct {
	calls       atomic.Int32
	orchStopped *atomic.Bool // whether the orchestrator had exited when Flush ran
	sawOrch     atomic.Bool
	deadline    time.Duration
	err         error

	closes        atomic.Int32
	closedFirst   atomic.Bool // Close ran before Flush
	closeDeadline time.Duration
}

func (f *recordingFlusher) Close(ctx context.Context) error {
	if f.calls.Load() == 0 {
		f.closedFirst.Store(true)
	}
	f.closes.Add(1)
	if d, ok := ctx.Deadline(); ok {
		f.closeDeadline = time.Until(d)
	}
	return nil
}

func (f *recordingFlusher) Flush(ctx context.Context) error {
	f.calls.Add(1)
	if f.orchStopped != nil {
		f.sawOrch.Store(f.orchStopped.Load())
	}
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	return f.err
}

// run()'s exit path must flush the per-issue logs exactly once, with a
// bounded ctx, after the orchestrator (and so its workers) has stopped —
// on every branch: no HTTP server, orchestrator exits first, and server
// exits first (the reload/graceful-shutdown branch).
func TestJoinRun_FlushesIssueLogsAfterTheOrchestratorStops(t *testing.T) {
	closedFlusher := make(chan struct{})
	close(closedFlusher)
	sentinel := errors.New("orch exit")

	cases := map[string]func(orchDone, srvDone chan error, stopped *atomic.Bool){
		"no server": func(orchDone, _ chan error, stopped *atomic.Bool) {
			stopped.Store(true)
			orchDone <- sentinel
		},
		"orchestrator first": func(orchDone, srvDone chan error, stopped *atomic.Bool) {
			stopped.Store(true)
			orchDone <- sentinel
			go func() {
				time.Sleep(20 * time.Millisecond) // server stops after the orchestrator
				srvDone <- nil
			}()
		},
		"server first": func(orchDone, srvDone chan error, stopped *atomic.Bool) {
			srvDone <- sentinel
			go func() {
				time.Sleep(20 * time.Millisecond)
				stopped.Store(true)
				orchDone <- nil
			}()
		},
	}
	for name, drive := range cases {
		t.Run(name, func(t *testing.T) {
			orchDone, srvDone := make(chan error, 1), make(chan error, 1)
			var stopped atomic.Bool
			f := &recordingFlusher{orchStopped: &stopped}
			drive(orchDone, srvDone, &stopped)

			var srv <-chan error = srvDone
			var ln net.Listener
			if name == "no server" {
				srv = nil
			} else {
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = l.Close() }()
				ln = l
			}

			if err := joinRun(orchDone, srv, ln, closedFlusher, f); !errors.Is(err, sentinel) {
				t.Fatalf("joinRun returned %v, want %v", err, sentinel)
			}
			if got := f.calls.Load(); got != 1 {
				t.Fatalf("Flush called %d times on shutdown, want 1", got)
			}
			if !f.sawOrch.Load() {
				t.Fatal("Flush ran before the orchestrator stopped")
			}
			if f.deadline <= 0 || f.deadline > issueLogFlushTimeout {
				t.Fatalf("Flush ctx deadline %v, want bounded by %v", f.deadline, issueLogFlushTimeout)
			}
			// M0-close fix-F: the writer is stopped after the flush, bounded,
			// so a reload's next Buffer never races the old writer.
			if got := f.closes.Load(); got != 1 || f.closedFirst.Load() {
				t.Fatalf("Close called %d times (before Flush: %v), want once after Flush", got, f.closedFirst.Load())
			}
			if f.closeDeadline <= 0 || f.closeDeadline > issueLogCloseTimeout {
				t.Fatalf("Close ctx deadline %v, want bounded by %v", f.closeDeadline, issueLogCloseTimeout)
			}
		})
	}
}

// A flush that times out is logged, not fatal: joinRun still returns run()'s
// own result.
func TestJoinRun_FlushTimeoutDoesNotChangeTheRunResult(t *testing.T) {
	closedFlusher := make(chan struct{})
	close(closedFlusher)
	orchDone := make(chan error, 1)
	orchDone <- nil
	f := &recordingFlusher{err: context.DeadlineExceeded}
	if err := joinRun(orchDone, nil, nil, closedFlusher, f); err != nil {
		t.Fatalf("joinRun returned %v, want nil", err)
	}
	if f.calls.Load() != 1 {
		t.Fatal("Flush was not called")
	}
	if f.closes.Load() != 1 {
		t.Fatal("Close was not called after a timed-out Flush")
	}
}
