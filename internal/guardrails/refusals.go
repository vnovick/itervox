package guardrails

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
)

// refusalLogName is the run's local refusal log in the runtime directory:
// one JSON Event per line, written by shims and hooks that have no daemon
// endpoint to report to (`itervox run`, #252, which opens no listener).
const refusalLogName = "refusals.jsonl"

// maxRefusalLogBytes bounds the log: a looping agent cannot fill the disk.
const maxRefusalLogBytes = 256 * 1024

// appendRefusal adds ev to the refusal log at path. Best effort, like the
// daemon report it stands in for: the refusal stands either way.
func appendRefusal(path string, ev Event) {
	if path == "" {
		return
	}
	if info, err := os.Lstat(path); err == nil && (info.Size() > maxRefusalLogBytes || !info.Mode().IsRegular()) {
		return
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|openNoFollow, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}

// RefusedApprovals lists the commands this run's shims refused because they
// needed an approval and no approval channel was available
// (KindApprovalUnavailable), in their redacted display form, first refusal
// first and each once. Empty when nothing was refused or the run reports to
// a daemon. Safe on nil.
func (r *Runtime) RefusedApprovals() []string {
	if r == nil || r.policy.Dir == "" {
		return nil
	}
	f, err := os.Open(filepath.Join(r.policy.Dir, refusalLogName))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), maxRefusalLogBytes)
	for sc.Scan() {
		var ev Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Kind != KindApprovalUnavailable || ev.Command == "" {
			continue
		}
		if !slices.Contains(out, ev.Command) {
			out = append(out, ev.Command)
		}
	}
	return out
}
