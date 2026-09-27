package workflow

import (
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"
)

// A registered self-write that the watcher never consumed expires
// selfWriteTTL after the path's most recent write, so a much later foreign
// edit that happens to reproduce the same transition still reloads.
func TestSelfWriteRegistryExpiresAfterTTL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	s, a := sha256.Sum256([]byte("S")), sha256.Sum256([]byte("A"))
	t0 := time.Now()

	recordSelfWriteAt(canonicalPath(path), s, a, t0)
	if consumeSelfWriteChain(path, s, a, t0.Add(selfWriteTTL+time.Second)) {
		t.Fatal("an expired self-write link still suppressed a reload")
	}

	// Within the TTL the same link suppresses exactly once.
	recordSelfWriteAt(canonicalPath(path), s, a, t0)
	if !consumeSelfWriteChain(path, s, a, t0.Add(time.Second)) {
		t.Fatal("a fresh self-write link did not suppress")
	}
	if consumeSelfWriteChain(path, s, a, t0.Add(2*time.Second)) {
		t.Fatal("a consumed self-write link suppressed a second time")
	}
}

// A burst of saves longer than the TTL does not age out its own early links:
// expiry is measured from the path's most recent write.
func TestSelfWriteRegistryTTLMeasuredFromNewestWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	h := func(s string) [32]byte { return sha256.Sum256([]byte(s)) }
	t0 := time.Now()
	key := canonicalPath(path)

	recordSelfWriteAt(key, h("S"), h("A"), t0)
	recordSelfWriteAt(key, h("A"), h("B"), t0.Add(selfWriteTTL-time.Second))
	if !consumeSelfWriteChain(path, h("S"), h("B"), t0.Add(selfWriteTTL+time.Second)) {
		t.Fatal("a burst's first link aged out while the burst was still being written")
	}
}

// The ring is bounded.
func TestSelfWriteRegistryIsBounded(t *testing.T) {
	path := canonicalPath(filepath.Join(t.TempDir(), "WORKFLOW.md"))
	now := time.Now()
	for i := range selfWriteCap + 50 {
		recordSelfWriteAt(path, sha256.Sum256([]byte{byte(i)}), sha256.Sum256([]byte{byte(i + 1)}), now)
	}
	selfWrites.mu.Lock()
	n := len(selfWrites.entries)
	selfWrites.mu.Unlock()
	if n > selfWriteCap {
		t.Fatalf("registry holds %d entries, cap is %d", n, selfWriteCap)
	}
}

// M1-close E3: an overflow of the registry evicts the oldest links, and one
// of them may be the only evidence of a foreign write (a link whose pre was
// not on the chain). The remaining links could then chain cleanly and the
// settle would suppress — swallowing that edit. After an overflow for a path,
// the next settle must reload instead (fail open).
func TestSelfWriteRegistryOverflowForcesReload(t *testing.T) {
	path := canonicalPath(filepath.Join(t.TempDir(), "WORKFLOW.md"))
	h := func(i int) [32]byte { return sha256.Sum256([]byte{byte(i), byte(i >> 8), byte(i >> 16)}) }
	now := time.Now()
	// Evidence link first: the daemon wrote over a foreign state F.
	recordSelfWriteAt(path, sha256.Sum256([]byte("foreign")), h(0), now)
	// Then a clean chain h(0)->h(1)->... long enough to evict the evidence.
	for i := range selfWriteCap {
		recordSelfWriteAt(path, h(i), h(i+1), now)
	}
	if consumeSelfWriteChain(path, h(0), h(selfWriteCap), now) {
		t.Fatal("a settle after a registry overflow was suppressed; the evicted links may have been the only evidence of a foreign edit")
	}
}
