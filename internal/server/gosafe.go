package server

import (
	"log/slog"
	"runtime/debug"

	"github.com/vnovick/itervox/internal/metrics"
)

// recoverServerGoroutine is internal/server's panic containment for one-shot
// background goroutines spawned by handlers (CORE-008). internal/server must
// not import internal/orchestrator (package order), so this is the local
// sibling of orchestrator.RecoverGoroutine. It must be deferred DIRECTLY at
// the top of the goroutine body. On a panic it logs the goroutine name, the
// panic and the stack, then runs onPanic — which publishes the task's
// failure outcome — under its own recover, so a faulty callback never
// re-crashes the process. `make no-bare-go` accepts it as containment.
func recoverServerGoroutine(name string, onPanic func(recovered any)) {
	r := recover()
	if r == nil {
		return
	}
	metrics.GoroutinePanic()
	slog.Error("goroutine panic recovered",
		"goroutine", name,
		"panic", r,
		"stack", string(debug.Stack()))
	if onPanic == nil {
		return
	}
	defer func() {
		if r2 := recover(); r2 != nil {
			metrics.GoroutinePanic()
			slog.Error("goroutine onPanic callback panicked", "goroutine", name, "panic", r2)
		}
	}()
	onPanic(r)
}
