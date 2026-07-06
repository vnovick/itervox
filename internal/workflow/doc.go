package workflow

// Doc is a typed builder for batched WORKFLOW.md edits. It collects multiple
// front-matter mutations (each a typed setter call) and writes them all in
// one atomic ApplyAndWriteFrontMatter call when Save is invoked. Use it for
// multi-field cascades that would otherwise issue several separate Patch*
// calls (e.g. AddSSHHost, which writes both `ssh_hosts` and
// `ssh_host_descriptions` keys).
//
// Why a builder rather than direct edits:
//   - One atomic write per logical operation: a SIGKILL between
//     `ssh_hosts` and `ssh_host_descriptions` patches used to leave the
//     two keys disagreeing about which hosts have descriptions. With Doc,
//     either both land or neither does (T-40 closed by T-30).
//   - One editMu acquisition: the per-path lock is held for the duration of
//     Save; all queued mutations apply to the same read snapshot, so there
//     is no read-modify-write window between mutations.
//   - Typed call sites: callers say `doc.SetSSHHosts(hosts, descs)` instead
//     of constructing JSON-marshalled patch values themselves.
//
// Doc is not safe for concurrent use by multiple goroutines on the same
// instance — the queue is mutated in-place. Concurrent calls on the SAME
// path but DIFFERENT Doc instances are serialized by the underlying
// editMu in ApplyAndWriteFrontMatter.
type Doc struct {
	path    string
	queue   []Mutator
	saveErr error
}

// NewDoc returns a Doc bound to the given WORKFLOW.md path. Each Set* call
// queues a mutation; Save flushes the queue in one atomic write.
func NewDoc(path string) *Doc {
	return &Doc{path: path}
}

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

// SetAgentStringSlice queues an update to a string-slice field under the
// agent: block (e.g. `ssh_hosts`). Empty slice removes the key.
//
// CASCADE WARNING (G-16, gaps_280426_2): for the `ssh_hosts` /
// `ssh_host_descriptions` pair, prefer SetSSHHosts which queues both keys
// in ONE Doc → ONE atomic Save. Calling SetAgentStringSlice("ssh_hosts",…)
// in one Doc and SetAgentStringMap("ssh_host_descriptions",…) in a SEPARATE
// Doc.Save reintroduces the torn-write window T-40 closed.
func (d *Doc) SetAgentStringSlice(key string, values []string) *Doc {
	d.queue = append(d.queue, MutateAgentStringSliceField(key, values))
	return d
}

// SetAgentStringMap queues an update to a string-map field under the
// agent: block (e.g. `ssh_host_descriptions`). Empty map removes the key.
//
// See SetAgentStringSlice's cascade warning — for the `ssh_host_descriptions`
// key, prefer SetSSHHosts to keep the cascade atomic.
func (d *Doc) SetAgentStringMap(key string, values map[string]string) *Doc {
	d.queue = append(d.queue, MutateAgentStringMapField(key, values))
	return d
}

// SetSSHHosts queues an atomic update of both `ssh_hosts` and
// `ssh_host_descriptions` keys. Convenience for the cascade that
// AddSSHHost / RemoveSSHHost previously issued as two separate Patch*
// calls (T-40 closed by T-30). Empty slice / nil map remove the keys.
func (d *Doc) SetSSHHosts(hosts []string, descriptions map[string]string) *Doc {
	d.SetAgentStringSlice("ssh_hosts", hosts)
	d.SetAgentStringMap("ssh_host_descriptions", descriptions)
	return d
}

// Save flushes the queued mutations in one atomic write via
// ApplyAndWriteFrontMatter. Holds editMu for the path across all queued
// mutations, so they share a single read snapshot. Returns the first error
// from any mutator (the file is left untouched on error).
//
// Calling Save with an empty queue is a no-op (no I/O, no lock).
func (d *Doc) Save() error {
	if len(d.queue) == 0 {
		return nil
	}
	if d.saveErr != nil {
		return d.saveErr
	}
	return ApplyAndWriteFrontMatter(d.path, d.queue...)
}
