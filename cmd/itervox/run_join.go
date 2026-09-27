package main

import (
	"context"
	"log/slog"
	"net"
	"time"
)

// issueLogFlushTimeout bounds the per-issue log flush on shutdown. Flush only
// waits for lines already queued in memory to reach disk, so this is generous
// for a healthy disk and short enough that a stalled one cannot hold up the
// exit.
const issueLogFlushTimeout = 2 * time.Second

// issueLogCloseTimeout bounds the wait for the log writer to stop after the
// flush: Close waits only for a disk write already in progress.
const issueLogCloseTimeout = time.Second

// issueLogFlusher is the slice of *logbuffer.Buffer that run() needs on its
// way out (a seam so the shutdown path is testable without a real run()).
type issueLogFlusher interface {
	Flush(ctx context.Context) error
	Close(ctx context.Context) error
}

// flushIssueLogs waits for every per-issue log line still queued for disk to
// be written, then stops the log writer, so a graceful shutdown or reload
// neither drops the queue's tail nor leaves the old writer appending while a
// reload's next Buffer counts the same files. Each step is bounded; a
// timeout, cancellation or unpersisted line is logged, never fatal. A hard
// crash skips this entirely — log durability is best-effort.
func flushIssueLogs(logs issueLogFlusher, flushTimeout, closeTimeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	if err := logs.Flush(ctx); err != nil {
		slog.Warn("run: per-issue log flush incomplete; some lines may be missing from the issue log files",
			"timeout", flushTimeout, "error", err)
	}
	cctx, ccancel := context.WithTimeout(context.Background(), closeTimeout)
	defer ccancel()
	if err := logs.Close(cctx); err != nil {
		slog.Warn("run: per-issue log writer did not stop in time; a disk write is still in progress",
			"timeout", closeTimeout, "error", err)
	}
}

// joinRun is run()'s exit path: it waits for the orchestrator (and the HTTP
// server, when one is running) to stop, joins the outbox flusher, and then —
// with every worker joined or past its grace (orch.Run joins its workers
// before it returns, CORE-026) — flushes the per-issue logs and stops their
// writer before run() returns to main(), which either exits or reloads.
//
// srvDone is nil when no HTTP server was started. The two server branches
// are symmetric: run() must not return while either component is live (see
// awaitStop and awaitOutboxFlusher for why).
func joinRun(orchDone, srvDone <-chan error, srvListener net.Listener, flusherDone <-chan struct{}, logs issueLogFlusher) error {
	var err error
	if srvDone == nil {
		err = <-orchDone
	} else {
		select {
		case err = <-orchDone:
			// Detach this run's server from the shared socket before
			// returning: the next run() serves on the same socket, and two
			// generations accepting at once would split requests between the
			// dying server and the new one. The explicit Close matters when
			// run() exits for a reason other than ctx cancellation
			// (orchestrator error) — the ctx-driven Shutdown in
			// serveOnListener never fires on that path.
			_ = srvListener.Close()
			if !awaitStop(srvDone, runShutdownGrace) {
				slog.Warn("run: http server did not stop within the shutdown grace of orchestrator exit",
					"grace", runShutdownGrace)
			}
		case err = <-srvDone:
			// Do NOT return while the orchestrator is still running. main()'s
			// reload loop calls run() again as soon as this returns, and a
			// second live orchestrator means a second outbox.New on the same
			// .itervox/outbox.json — two handles each rewriting the whole
			// file on every persist, silently erasing each other's durable
			// entries. On a reload this is the branch that actually fires:
			// shutting the HTTP generation down is a channel close, while
			// orch.Run is still draining its WaitGroups.
			if !awaitStop(orchDone, runShutdownGrace) {
				slog.Warn("run: orchestrator did not stop within the shutdown grace of http server exit; "+
					"a reload now would run two orchestrators against one outbox file",
					"grace", runShutdownGrace)
			}
		}
	}
	awaitOutboxFlusher(flusherDone)
	flushIssueLogs(logs, issueLogFlushTimeout, issueLogCloseTimeout)
	return err
}
