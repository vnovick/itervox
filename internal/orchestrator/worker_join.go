package orchestrator

import (
	"slices"
	"sync"
	"time"
)

// CORE-026 — Run joins its agent workers before it returns.
//
// Every `go o.runWorker(...)` is preceded, on the event loop, by
// o.workersWg.Add(identifier), and runWorker's outermost defer is
// o.workersWg.Done(identifier). After the event loop exits, Run waits for the
// group with a bounded grace (workerJoinGrace) before its cleanup-group
// waits, so a worker's last acts after RunTurn returns — its final per-issue
// log lines, post-run tracker comments, a git push under postRunTimeout — no
// longer overlap run()'s shutdown work (flushing the per-issue logs, a reload
// starting the next generation in the same repo).
//
// The join cannot deadlock: it runs after the loop has stopped reading
// o.events, and every blocking worker send (sendExit, the input-required
// send) also selects on the Run ctx, which is done by then. On grace expiry
// Run logs the still-running identifiers at Warn and returns; the next
// generation may overlap them, exactly as before CORE-026. Workers still only
// send events; nothing here touches State.

// defaultWorkerJoinGrace bounds Run's wait for its workers. It covers the
// agent's 5 s kill window plus a margin for post-run tracker writes, and
// stays well inside main()'s 30 s --shutdown-grace so run() still reaches its
// per-issue log flush before a forced exit.
const defaultWorkerJoinGrace = 20 * time.Second

// workerGroup is a WaitGroup that remembers which identifiers are still
// running and whose Wait is bounded. The zero value is ready to use.
type workerGroup struct {
	mu   sync.Mutex
	live map[string]int
	n    int
	idle chan struct{} // non-nil while n > 0; closed when n returns to 0
}

// Add records a worker for identifier about to be launched.
func (g *workerGroup) Add(identifier string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.live == nil {
		g.live = make(map[string]int)
	}
	g.live[identifier]++
	g.n++
	if g.n == 1 {
		g.idle = make(chan struct{})
	}
}

// Done records that a worker for identifier has returned.
func (g *workerGroup) Done(identifier string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.live[identifier]--; g.live[identifier] <= 0 {
		delete(g.live, identifier)
	}
	g.n--
	if g.n == 0 && g.idle != nil {
		close(g.idle)
		g.idle = nil
	}
}

// Wait blocks until every added worker is done or grace has passed, and
// returns the identifiers still running (sorted; nil when all are done).
func (g *workerGroup) Wait(grace time.Duration) []string {
	g.mu.Lock()
	idle := g.idle
	g.mu.Unlock()
	if idle == nil {
		return nil
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-idle:
		return nil
	case <-timer.C:
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	still := make([]string, 0, len(g.live))
	for id := range g.live {
		still = append(still, id)
	}
	slices.Sort(still)
	return still
}

// SetWorkerJoinGrace overrides how long Run waits for its workers after the
// event loop stops (default defaultWorkerJoinGrace; <= 0 restores it). Must be
// called before Run.
func (o *Orchestrator) SetWorkerJoinGrace(d time.Duration) {
	o.workerJoinGrace = d
}

func (o *Orchestrator) workerJoinGraceOrDefault() time.Duration {
	if o.workerJoinGrace > 0 {
		return o.workerJoinGrace
	}
	return defaultWorkerJoinGrace
}
