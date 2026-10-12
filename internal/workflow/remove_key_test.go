package workflow

import (
	"crypto/sha256"
	"os"
	"strings"
	"testing"
	"time"
)

const removeKeyWorkflow = `---
# Project workflow — hand-written comments must survive.
itervox_schema_version: 2
tracker:
  kind: linear
  max_retries: 9 # a tracker key of the same name is not agent.max_retries
agent:
  command: claude
  # The retry budget.
  max_retries: 4 # inline comment goes with its line
  ssh_hosts:
    - host-a
    # a comment inside the list
    - host-b
  ssh_host_descriptions:
    host-a: "first"
  dispatch_strategy: round-robin
server:
  port: 8090
  allow_unauthenticated_lan: true
workspace:
  root: ./ws
  auto_clear: true
---

Prompt body with agent:
  max_retries: 1
`

// removeLines returns content with the given exact lines (each once) removed.
func removeLines(t *testing.T, content string, lines ...string) string {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(content, l+"\n") {
			t.Fatalf("fixture has no line %q", l)
		}
		content = strings.Replace(content, l+"\n", "", 1)
	}
	return content
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A nested scalar key: only its own line goes; the comment above it, the
// tracker key of the same name and the body's look-alike stay.
func TestRemoveFrontMatterKeysRemovesNestedScalarOnly(t *testing.T) {
	path := writeTempWorkflow(t, removeKeyWorkflow)
	if err := RemoveFrontMatterKeys(path, []string{"agent.max_retries"}); err != nil {
		t.Fatal(err)
	}
	want := removeLines(t, removeKeyWorkflow, "  max_retries: 4 # inline comment goes with its line")
	if got := readFile(t, path); got != want {
		t.Fatalf("file differs beyond the removed line:\n got: %q\nwant: %q", got, want)
	}
}

// A block list and a nested map: the key line and every value line (the
// comment inside the list included) go; the neighbours stay in order.
func TestRemoveFrontMatterKeysRemovesBlockValues(t *testing.T) {
	path := writeTempWorkflow(t, removeKeyWorkflow)
	if err := RemoveFrontMatterKeys(path, []string{"agent.ssh_hosts", "agent.ssh_host_descriptions"}); err != nil {
		t.Fatal(err)
	}
	want := removeLines(t, removeKeyWorkflow,
		"  ssh_hosts:", "    - host-a", "    # a comment inside the list", "    - host-b",
		"  ssh_host_descriptions:", `    host-a: "first"`)
	if got := readFile(t, path); got != want {
		t.Fatalf("got: %q\nwant: %q", got, want)
	}
}

// An absent key (or an absent parent) is a no-op that does not even write.
func TestRemoveFrontMatterKeysAbsentKeyIsNoOp(t *testing.T) {
	path := writeTempWorkflow(t, removeKeyWorkflow)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFrontMatterKeys(path, []string{"agent.max_turns", "polling.interval_ms", "workspace.root.deeper"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != removeKeyWorkflow {
		t.Fatalf("an absent key changed the file:\n%s", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(old) {
		t.Fatal("an absent key must not rewrite the file")
	}
}

// Both spellings of one setting are removed when both are named (the
// adapter passes the alias from config.SettingAlias); the rest of server:
// stays.
func TestRemoveFrontMatterKeysRemovesAliasSpelling(t *testing.T) {
	path := writeTempWorkflow(t, removeKeyWorkflow)
	if err := RemoveFrontMatterKeys(path, []string{"server.allow_unauthenticated", "server.allow_unauthenticated_lan"}); err != nil {
		t.Fatal(err)
	}
	want := removeLines(t, removeKeyWorkflow, "  allow_unauthenticated_lan: true")
	if got := readFile(t, path); got != want {
		t.Fatalf("got: %q\nwant: %q", got, want)
	}
}

// Four-space indentation and a top-level key both work.
func TestMutateRemoveKeyFourSpaceIndentAndTopLevel(t *testing.T) {
	front := []string{
		"tracker:",
		"    kind: linear",
		"agent:",
		"    command: claude",
		"    auto_review: true",
		"polling:",
		"    interval_ms: 5000",
	}
	got, err := MutateRemoveKeys("agent.auto_review", "polling")(append([]string{}, front...))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tracker:", "    kind: linear", "agent:", "    command: claude"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A flow-style mapping cannot be edited line-wise: the mutator refuses
// instead of reporting success while the key stays set.
func TestMutateRemoveKeyRefusesFlowStyle(t *testing.T) {
	front := []string{"agent: {command: claude, max_retries: 3}"}
	if _, err := MutateRemoveKey("agent.max_retries")(front); err == nil {
		t.Fatal("want an error: the flow-style key is still set")
	}
}

// The removal is a self-write: this process's watcher must not reload for it
// (CORE-116), exactly like the Patch helpers' writes.
func TestRemoveFrontMatterKeysIsASelfWrite(t *testing.T) {
	path := writeTempWorkflow(t, removeKeyWorkflow)
	before := sha(removeKeyWorkflow)
	if err := RemoveFrontMatterKeys(path, []string{"agent.max_retries"}); err != nil {
		t.Fatal(err)
	}
	if !consumeSelfWriteChain(path, before, sha(readFile(t, path)), time.Now()) {
		t.Fatal("the key removal was not registered as a self-write; the watcher would reload")
	}
}

func sha(s string) [32]byte { return sha256.Sum256([]byte(s)) }
