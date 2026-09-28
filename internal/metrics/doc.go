// Package metrics exposes itervox's Prometheus text endpoint (CORE-045).
//
// Two kinds of data feed it:
//
//   - Process-wide counters (counters.go) incremented at
//     their event sites: worker exits, tracker HTTP calls, recovered goroutine
//     panics, and dropped orchestrator events. They are package-level atomics
//     on purpose — the orchestrator is rebuilt on every WORKFLOW.md reload, and
//     a Prometheus counter must be monotonic for the life of the process, so
//     an injected per-generation recorder would reset them on every reload.
//     Incrementing never takes cfgMu (the package imports nothing but the
//     standard library) and is cheap enough to stay on when the endpoint is
//     off.
//   - A View of gauges and snapshot-owned counters, built by cmd/itervox from
//     orchestrator.Snapshot() (snapMu only), the outbox's own snapshot and the
//     tracker rate-limit gate. This package never imports orchestrator,
//     server or tracker: the caller injects a func() View.
//
// The text exposition format is written by hand (WriteText). The daemon had no
// Prometheus client dependency and the format needs ~150 lines; pulling in
// client_golang (and its protobuf, procfs and common/expfmt transitive tree)
// for a dozen series would roughly double the module graph of a single static
// binary. The format is pinned by a strict parser in exposition_test.go.
package metrics
