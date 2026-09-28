package server

import (
	"testing"

	"github.com/vnovick/itervox/internal/domain"
)

// TestSubLogEpochKeyedOnSession (M6-close LOW): the sublog epoch identifies
// the set of session files by the first entry's session id, not by the
// re-encoded first entry — a re-render of the same first line (a parser
// change, a detail field filled in later) must not look like a replaced set,
// and a different session must.
func TestSubLogEpochKeyedOnSession(t *testing.T) {
	a := []domain.IssueLogEntry{{Event: "text", Message: "hello", SessionID: "sess-1", Time: "10:00:00"}}
	aReRendered := []domain.IssueLogEntry{{Event: "text", Message: "hello (re-rendered)", Detail: "now with detail", SessionID: "sess-1", Time: "10:00:00"}}
	b := []domain.IssueLogEntry{{Event: "text", Message: "hello", SessionID: "sess-2", Time: "10:00:00"}}
	if subLogEpoch(a) != subLogEpoch(aReRendered) {
		t.Fatal("a re-rendered first entry of the same session changed the epoch")
	}
	if subLogEpoch(a) == subLogEpoch(b) {
		t.Fatal("a different session must have a different epoch")
	}
	if subLogEpoch(nil) != 0 || subLogEpoch(a) == 0 {
		t.Fatal("0 is reserved for the empty set")
	}
}
