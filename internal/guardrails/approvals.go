package guardrails

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Approval statuses reported to a waiting shim.
const (
	// StatusPending: no human has answered yet.
	StatusPending = "pending"
	// StatusApproved: a human approved; the shim that reads it runs the
	// command, and the approval is spent by that read.
	StatusApproved = "approved"
	// StatusRejected: a human rejected; Message holds their reply.
	StatusRejected = "rejected"
	// StatusUnknown: no such request (spent, the run ended, or the daemon
	// restarted). The shim refuses.
	StatusUnknown = "unknown"
)

// ApprovalRequest is what a shim sends when a command needs approval. Key
// identifies the exact command (a digest of its arguments and directory) so
// a repeat of the same command joins the pending request instead of asking
// again; Command is its redacted display form.
type ApprovalRequest struct {
	Key     string `json:"key"`
	Command string `json:"command"`
}

// ApprovalStatus is an approval request's state as a shim sees it.
type ApprovalStatus struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// Event is one refused operation a shim or hook reports to the daemon. The
// command is already redacted by the reporter and is redacted again by the
// daemon before it is logged or stored.
type Event struct {
	Kind    string `json:"kind"`
	Tool    string `json:"tool"`
	Command string `json:"command"`
}

// Approval is one approval request held by Approvals.
type Approval struct {
	ID         string
	Identifier string
	RunID      string
	Key        string
	Command    string
	Status     string
	Message    string
	CreatedAt  time.Time
}

// Approvals holds the daemon's approval requests. Concurrency-safe: HTTP
// handlers create and poll requests, the orchestrator's event loop decides
// them. It is not orchestrator State; the input-required entry that shows a
// request to a human is.
//
// Exactly once: an approved request is removed by the first read that sees
// it approved (Status, or a Request for the same command), so one approval
// runs the command once however many shims poll.
type Approvals struct {
	mu    sync.Mutex
	byID  map[string]*Approval
	now   func() time.Time
	newID func() string
}

// NewApprovals returns an empty Approvals.
func NewApprovals() *Approvals {
	return &Approvals{byID: map[string]*Approval{}, now: time.Now, newID: randomID}
}

func randomID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Request records a request for identifier's run runID, or joins the
// existing one for the same command. created reports a new request (the
// caller then raises it to a human). A request for the same command that
// was approved and not yet run is spent here and returned approved.
func (a *Approvals) Request(identifier, runID string, req ApprovalRequest) (st ApprovalStatus, created bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, r := range a.byID {
		if r.Identifier != identifier || r.RunID != runID || r.Key != req.Key {
			continue
		}
		switch r.Status {
		case StatusApproved:
			delete(a.byID, id)
			return ApprovalStatus{ID: id, Status: StatusApproved, Message: r.Message}, false
		case StatusPending:
			return ApprovalStatus{ID: id, Status: StatusPending}, false
		default: // rejected and never read: a new attempt asks again
			delete(a.byID, id)
		}
	}
	id := a.newID()
	a.byID[id] = &Approval{ID: id, Identifier: identifier, RunID: runID, Key: req.Key,
		Command: req.Command, Status: StatusPending, CreatedAt: a.now()}
	return ApprovalStatus{ID: id, Status: StatusPending}, true
}

// Status returns request id of identifier's run runID. A decided request is
// removed by this read: approved is returned once, then unknown.
func (a *Approvals) Status(identifier, runID, id string) ApprovalStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.byID[id]
	if !ok || r.Identifier != identifier || r.RunID != runID {
		return ApprovalStatus{ID: id, Status: StatusUnknown}
	}
	if r.Status != StatusPending {
		delete(a.byID, id)
	}
	return ApprovalStatus{ID: id, Status: r.Status, Message: r.Message}
}

// Get returns a copy of request id.
func (a *Approvals) Get(id string) (Approval, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.byID[id]
	if !ok {
		return Approval{}, false
	}
	return *r, true
}

// Decide answers pending request id. It reports false when the request is
// gone or already decided.
func (a *Approvals) Decide(id string, approved bool, message string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.byID[id]
	if !ok || r.Status != StatusPending {
		return false
	}
	r.Status = StatusRejected
	if approved {
		r.Status = StatusApproved
	}
	r.Message = message
	return true
}

// Remove deletes request id.
func (a *Approvals) Remove(id string) {
	a.mu.Lock()
	delete(a.byID, id)
	a.mu.Unlock()
}

// Drop removes every request of identifier (its run ended) and returns
// how many were removed. A shim still polling then sees unknown and
// refuses.
func (a *Approvals) Drop(identifier string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for id, r := range a.byID {
		if r.Identifier == identifier {
			delete(a.byID, id)
			n++
		}
	}
	return n
}
