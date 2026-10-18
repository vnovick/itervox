package workflow

import (
	"context"
	"log/slog"
)

// SetMaxConcurrentAgents queues an update to the `max_concurrent_agents`
// integer field in the front matter. Equivalent to PatchIntField but
// composable with other Set* calls in the same Save.
func (d *Doc) SetMaxConcurrentAgents(n int) *Doc {
	d.queue = append(d.queue, MutateIntField("max_concurrent_agents", n))
	return d
}

// SetAgentString queues an update to a top-level string field under the
// agent: block (e.g. `dispatch_strategy`).
func (d *Doc) SetAgentString(key, value string) *Doc {
	d.queue = append(d.queue, MutateAgentStringField(key, value))
	return d
}

// SetAgentBool queues an update to a top-level boolean field under the
// agent: block. enabled=false removes the key entirely (matching the
// existing PatchAgentBoolField semantics).
func (d *Doc) SetAgentBool(key string, enabled bool) *Doc {
	d.queue = append(d.queue, MutateAgentBoolField(key, enabled))
	return d
}

// PatchAgentStringSliceField sets or removes a string-slice key under the
// agent: block of the YAML front matter. Empty values remove the key.
//
// Routed through ApplyAndWriteFrontMatter (CORE-006); see PatchAgentBoolField.
func PatchAgentStringSliceField(path, key string, values []string) error {
	return ApplyAndWriteFrontMatter(path, MutateAgentStringSliceField(key, values))
}

// PatchAgentStringMapField replaces or removes a string map under the agent:
// block of the YAML front matter. Empty maps remove the key entirely.
//
// Routed through ApplyAndWriteFrontMatter (CORE-006); see PatchAgentBoolField.
func PatchAgentStringMapField(path, key string, values map[string]string) error {
	return ApplyAndWriteFrontMatter(path, MutateAgentStringMapField(key, values))
}

// PatchProfilesBlock replaces (or inserts) the agent.profiles block in the YAML
// front matter of the file at path. profiles maps profile name → ProfileEntry.
// Passing nil or an empty map removes the profiles block entirely.
// The rest of the file (other keys, comments, prompt body) is preserved byte-for-byte.
func PatchProfilesBlock(path string, profiles map[string]ProfileEntry) error {
	return ApplyAndWriteFrontMatter(path, MutateProfilesBlock(profiles))
}

// WriteAndReload is ApplyAndWriteFrontMatter for a value the running daemon
// can only pick up by reloading WORKFLOW.md (a consumer built once per run
// generation, e.g. the tracker client's active/terminal state lists). The
// write is NOT registered as a self-write, so the watcher reloads after it
// exactly as it would for an operator edit.
func WriteAndReload(path string, mutators ...Mutator) error {
	return applyAndWrite(path, false, false, mutators)
}

// The per-field patchers below were production code until #158 replaced them
// with the generic SetFrontMatterKeys / MutateSetKey. Their tests are kept
// (concurrency, lock, CORE-125 and indentation coverage) and now exercise the
// generic writer through these thin test-only wrappers.

// PatchTrackerStates sets tracker.active_states, tracker.terminal_states and
// tracker.completion_state in one write.
func PatchTrackerStates(path string, active, terminal []string, completion string) error {
	return ApplyAndWriteFrontMatter(path, MutateTrackerStates(active, terminal, completion))
}

// MutateTrackerStates is the Mutator form of PatchTrackerStates.
func MutateTrackerStates(active, terminal []string, completion string) Mutator {
	return MutateSetKeys(
		KeyValue{Key: "tracker.active_states", Value: active},
		KeyValue{Key: "tracker.terminal_states", Value: terminal},
		KeyValue{Key: "tracker.completion_state", Value: completion},
	)
}

// PatchTrackerFailedState sets tracker.failed_state; "" removes it.
func PatchTrackerFailedState(path, state string) error {
	return SetFrontMatterKeys(path, KeyValue{Key: "tracker.failed_state", Value: emptyAsNil(state)})
}

// PatchDependenciesStringField sets dependencies.<key>; "" removes it.
func PatchDependenciesStringField(path, key, value string) error {
	return ApplyAndWriteFrontMatter(path, MutateSetKey("dependencies."+key, emptyAsNil(value)))
}

func emptyAsNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Watch monitors path for changes by polling every second with a stamp of
// {mtime, size, sha256}. Any stamp change — including a rewrite of identical
// bytes with a new mtime — starts the debounce; once the file has stayed
// unchanged for debounceInterval it "settles" and onChange is called.
//
// Self-write suppression (CORE-116): at settle time, if the file moved from
// the content Watch last settled on to the current content purely through
// writes this process made via the package's locked patchers (see
// selfwrite.go), onChange is NOT called — the daemon already applied those
// values in memory, and a reload would cancel every in-flight agent turn for
// nothing. Operator edits, edits by other processes, and any mix of the two
// with daemon writes still call onChange.
//
// Watch's baseline is its own first reading. The daemon uses WatchFrom with
// the hash config.Load parsed instead, so nothing that lands between the load
// and that first reading can slip into the baseline.
//
// Blocks until ctx is cancelled. Returns any setup or context error.
func Watch(ctx context.Context, path string, onChange func()) error {
	first, err := stampOf(path, fileStamp{})
	if err != nil {
		slog.Warn("workflow watcher: initial stat failed", "path", path, "error", err)
	}
	return watch(ctx, path, first.hash, first, false, onChange)
}
