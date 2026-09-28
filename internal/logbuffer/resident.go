package logbuffer

import "strings"

// ResidentIdentifiers returns the identifiers whose in-memory window
// currently holds at least one line. Unlike Identifiers it never touches the
// disk, so the orchestrator's event loop may call it every tick (CORE-035's
// janitor eviction). The returned slice is unsorted.
func (b *Buffer) ResidentIdentifiers() []string {
	var ids []string
	b.issues.Range(func(key, v any) bool {
		ib := v.(*issueBuf)
		ib.mu.RLock()
		resident := len(ib.lines) > 0
		ib.mu.RUnlock()
		if resident {
			ids = append(ids, key.(string))
		}
		return true
	})
	return ids
}

// Subagent marker messages logged by the agent runners when the agent spawns
// a subagent (internal/agent: `log.Info(logPrefix+": subagent", ...)`, which
// slog renders as msg="claude: subagent" / msg="codex: subagent").
const (
	claudeSubagentMarker = `"claude: subagent"`
	codexSubagentMarker  = `"codex: subagent"`
)

// IsSubagentMarker reports whether a buffered log line records a subagent
// spawn. Same predicate the dashboard snapshot used to apply to every
// retained line on every snapshot.
func IsSubagentMarker(line string) bool {
	return strings.Contains(line, claudeSubagentMarker) || strings.Contains(line, codexSubagentMarker)
}

// SubagentCount returns the number of subagent marker lines in identifier's
// retained in-memory window, in O(1): the count is maintained incrementally
// by Add and dropped with the window (CORE-035). It never reads the disk; an
// identifier with an empty or no window reports 0.
func (b *Buffer) SubagentCount(identifier string) int {
	v, ok := b.issues.Load(identifier)
	if !ok {
		return 0
	}
	ib := v.(*issueBuf)
	ib.mu.RLock()
	defer ib.mu.RUnlock()
	return ib.subagents
}
