package main

import (
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/server"
)

// guardrails.lanes.split: `itervox watch` prints a stack_split feed event
// with its order and reason.
func TestWatchPrintsStackSplitEvent(t *testing.T) {
	w := &watcher{}
	line, command := w.eventText(server.FeedEvent{Seq: 4, Kind: "stack_split", Identifier: "ENG-1",
		Payload: server.FeedPayload{Outcome: "code_first", Reason: "the signed checks pass on the code change alone"}})
	if !strings.Contains(line, "stack split: code first (the signed checks pass on the code change alone)") {
		t.Fatalf("line = %q", line)
	}
	if command != "" {
		t.Fatalf("command = %q; a decision asks for nothing", command)
	}
}
