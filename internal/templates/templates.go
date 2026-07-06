// Package templates embeds static markdown blocks appended by itervox init
// to every generated WORKFLOW.md. The dynamic portions of WORKFLOW.md are
// built inline in cmd/itervox/main.go; only fully-static sections live here.
package templates

import _ "embed"

// HumanInput is the static markdown block that instructs agents how and when
// to emit the <!-- itervox:needs-input --> sentinel. Appended to every
// generated WORKFLOW.md by `itervox init` so the contract reaches real
// projects instead of living only in this package.
//
//go:embed human_input.md
var HumanInput []byte

// Quickstart is a complete WORKFLOW.md suitable for a no-API-key, no-tracker
// trial of itervox. Tracker is in-memory (synthetic issues populated by
// tracker.GenerateDemoIssues), agent is a no-op echo, server binds to
// loopback. Replaces the formerly-builtin `--demo` flag — copy it next to
// the daemon and run `itervox -workflow quickstart-WORKFLOW.md`.
//
//go:embed quickstart.md
var Quickstart []byte
