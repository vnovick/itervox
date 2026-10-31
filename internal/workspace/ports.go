package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// #250 — ITERVOX_PORT: one TCP port per issue workspace, picked from
// workspace.port_range, so agents and hooks running dev servers in parallel
// do not collide.
//
// Ownership: the port belongs to the workspace, not to orchestrator State.
// A workspace is created and removed off the event loop (worker goroutines,
// the auto-clear goroutine, startup cleanup, `itervox clear`), and the hooks
// that need the port run on those same goroutines. So the allocator is a
// workspace-level ledger guarded by portsMu, in the same way the shared
// clone's fetches are serialised by lockRepo, rather than event-loop state:
// no orchestrator decision reads it, and an event round trip would make a
// worker block on the loop before its after_create hook.
//
// Persistence: the ledger is .itervox-ports.json under workspace.root
// (under <root>/worktrees in worktree mode), read and written (atomically)
// on every change under portsMu, so a restarted daemon and every Manager of
// this process (one per config generation) agree. An entry whose workspace
// directory no longer exists is dropped, so a workspace removed by any path
// releases its port.

// PortsFile is the port ledger's name under workspace.root.
const PortsFile = ".itervox-ports.json"

// portsMu serialises every read-modify-write of a port ledger in this
// process.
var portsMu sync.Mutex

// portFree reports whether port can be bound on the loopback interface now.
// A variable so tests can make the probe deterministic.
var portFree = func(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

type portLedger struct {
	Ports map[string]int `json:"ports"`
}

// portsPath is the ledger's path: under <root>/worktrees in worktree mode
// (in a legacy setup the root is the operator's own clone, which should not
// get a stray file), else under the root.
func (m *Manager) portsPath() string {
	if m.cfg.Workspace.Worktree {
		return filepath.Join(m.cfg.Workspace.Root, "worktrees", PortsFile)
	}
	return filepath.Join(m.cfg.Workspace.Root, PortsFile)
}

func readPortLedger(path string) map[string]int {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("workspace: cannot read the port ledger; starting a new one", "path", path, "error", err)
		}
		return map[string]int{}
	}
	var l portLedger
	if err := json.Unmarshal(data, &l); err != nil || l.Ports == nil {
		if err != nil {
			slog.Warn("workspace: the port ledger does not parse; starting a new one", "path", path, "error", err)
		}
		return map[string]int{}
	}
	return l.Ports
}

func writePortLedger(path string, ports map[string]int) error {
	data, err := json.MarshalIndent(portLedger{Ports: ports}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfs.WriteFile(path, append(data, '\n'), 0o644)
}

// pruneGonePorts drops the entries (other than keep) whose workspace
// directory no longer exists and reports whether it dropped any.
func (m *Manager) pruneGonePorts(ports map[string]int, keep string) bool {
	changed := false
	for id := range ports {
		if id == keep {
			continue
		}
		if _, err := os.Stat(m.ResolvePath(id)); errors.Is(err, os.ErrNotExist) {
			delete(ports, id)
			changed = true
		}
	}
	return changed
}

// AssignPort returns identifier's workspace port: the one already recorded
// while its workspace exists, else the lowest port in workspace.port_range
// that no other workspace holds and that can be bound on 127.0.0.1 now. The
// choice is persisted before it is returned.
func (m *Manager) AssignPort(identifier string) (int, error) {
	lo, hi := m.cfg.Workspace.PortMin, m.cfg.Workspace.PortMax
	if lo <= 0 || hi < lo {
		return 0, nil // no range configured (a Config not built by config.Load)
	}
	portsMu.Lock()
	defer portsMu.Unlock()
	path := m.portsPath()
	ports := readPortLedger(path)
	changed := m.pruneGonePorts(ports, identifier)
	if p, ok := ports[identifier]; ok && p >= lo && p <= hi {
		if changed {
			if err := writePortLedger(path, ports); err != nil {
				slog.Warn("workspace: cannot write the port ledger", "path", path, "error", err)
			}
		}
		return p, nil
	}
	taken := make(map[int]bool, len(ports))
	for id, p := range ports {
		if id != identifier {
			taken[p] = true
		}
	}
	for p := lo; p <= hi; p++ {
		if taken[p] || !portFree(p) {
			continue
		}
		next := maps.Clone(ports)
		next[identifier] = p
		if err := writePortLedger(path, next); err != nil {
			return 0, fmt.Errorf("workspace: persist port: %w", err)
		}
		return p, nil
	}
	return 0, fmt.Errorf("workspace: no free port in workspace.port_range %d-%d", lo, hi)
}

// WorkspacePort returns identifier's recorded workspace port, or 0 when it
// has none or its workspace no longer exists. Safe from any goroutine.
func (m *Manager) WorkspacePort(identifier string) int {
	portsMu.Lock()
	defer portsMu.Unlock()
	p := readPortLedger(m.portsPath())[identifier]
	if p == 0 {
		return 0
	}
	if _, err := os.Stat(m.ResolvePath(identifier)); err != nil {
		return 0
	}
	return p
}

// releasePort forgets identifier's port. Called once its workspace is gone.
func (m *Manager) releasePort(identifier string) {
	portsMu.Lock()
	defer portsMu.Unlock()
	path := m.portsPath()
	ports := readPortLedger(path)
	if _, ok := ports[identifier]; !ok {
		return
	}
	delete(ports, identifier)
	if err := writePortLedger(path, ports); err != nil {
		slog.Warn("workspace: cannot write the port ledger", "path", path, "error", err)
	}
}
