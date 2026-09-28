package workflow

import (
	"crypto/sha256"
	"path/filepath"
	"sync"
	"time"
)

// Self-write suppression (CORE-116).
//
// Every dashboard or TUI settings save rewrites WORKFLOW.md AND applies the
// new value in memory under cfgMu. Without suppression the watcher cannot
// tell that write from an operator edit, so ~3 s later it cancels the run
// context — killing every in-flight agent turn — to "reload" a value that is
// already live.
//
// The registry records, for every write made by this process through the
// locked patchers, the sha256 of the bytes the patcher READ (pre) and of the
// bytes it WROTE (post). The watcher consults it only at settle time, after
// the debounce: it suppresses the reload iff the file moved from the content
// it last settled on (its baseline) to the content it settled on now purely
// through a chain of this process's own writes — baseline → post₁ → post₂ …
// → current, each link's pre equal to the previous link's post.
//
// Matching on the (pre, post) chain rather than on "current bytes equal the
// last bytes we wrote" is what keeps operator edits from being swallowed:
//
//   - daemon write, then an operator edit before settle: the settled bytes
//     are not the post of any link → reload.
//   - operator edit, then a daemon write before settle: the daemon's
//     read-modify-write carries the operator's edit forward, so the settled
//     bytes DO equal the daemon's post — but that link's pre is the
//     operator's bytes, not the baseline, so the chain breaks → reload.
//   - an edit by ANOTHER process (itervox init --update, itervox models
//     refresh — CORE-149) never enters this process's registry → reload.
//
// Links are one-shot: a suppressed settle consumes the links it used, so a
// later foreign write that happens to reproduce the same bytes still reloads.
// Unconsumed links expire selfWriteTTL after the most recent write to the
// same path (so a long burst of saves does not age out its own early links),
// and the registry is capped at selfWriteCap entries purely as a memory bound
// (see selfWriteCap: sized so a real burst never overflows it).
//
// The registry is process-local by design: it is not State, not a cfgMu
// field, and not persisted.

// selfWriteCap is a memory bound only, not part of the matching logic
// (M1-close D5). Links leave the registry when a settle consumes them (or
// drops them all on a reload) and through selfWriteTTL, so between settles it
// holds only the links written since the last one — and the watcher cannot
// settle until the file has been quiet for debounceInterval. It was 16, and a
// burst of 17+ saves with no debounce-length pause (holding the TUI's worker
// +/- key auto-repeats at ~30 saves/s) evicted the chain's first links and
// caused one spurious reload. At 4096 an overflow needs 4096 saves with no
// 2 s pause (over two minutes of continuous key repeat). An overflow evicts
// the oldest links, and one of them may be the only evidence of a foreign
// write, so the remaining links alone could chain cleanly and swallow that
// edit. Eviction therefore marks the path overflowed, and the next settle
// (or WatchFrom's first reading) for it reloads unconditionally (M1-close
// E3). Past the cap, the outcome is at most one needless reload, never a
// swallowed edit. Worst case ~4096 x 100 B.
const selfWriteCap = 4096

// selfWriteTTL is a var so tests can shrink it; see export_test.go.
var selfWriteTTL = 30 * time.Second

type selfWrite struct {
	path      string
	pre, post [32]byte
	at        time.Time
}

var selfWrites struct {
	mu      sync.Mutex
	entries []selfWrite // oldest first
	// overflowed marks paths that lost links to the selfWriteCap eviction
	// since their last settle or load (M1-close E3). The evicted links may
	// have been the only evidence of a foreign write, so the next settle for
	// such a path reloads instead of trusting the links that remain.
	overflowed map[string]bool
}

// canonicalPath normalises a WORKFLOW.md path so the patcher and the watcher
// agree on the registry key even when one is given a relative path or a
// path through a symlink (macOS /var → /private/var).
func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	// The file may not exist yet; resolve the directory instead.
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return abs
}

// recordSelfWrite registers a successful write of post over pre at path.
// Called by writeLocked after atomicfs.WriteFile returns, i.e. after the
// rename — the watcher only evaluates at settle time (>= one debounce after
// the last observed change), so registering after the rename cannot lose a
// race with the poller.
func recordSelfWrite(path string, pre, post []byte) {
	recordSelfWriteAt(canonicalPath(path), sha256.Sum256(pre), sha256.Sum256(post), time.Now())
}

func recordSelfWriteAt(path string, pre, post [32]byte, now time.Time) {
	selfWrites.mu.Lock()
	defer selfWrites.mu.Unlock()
	pruneSelfWritesLocked(now)
	selfWrites.entries = append(selfWrites.entries, selfWrite{path: path, pre: pre, post: post, at: now})
	if over := len(selfWrites.entries) - selfWriteCap; over > 0 {
		if selfWrites.overflowed == nil {
			selfWrites.overflowed = make(map[string]bool)
		}
		for _, e := range selfWrites.entries[:over] {
			selfWrites.overflowed[e.path] = true
		}
		selfWrites.entries = append(selfWrites.entries[:0:0], selfWrites.entries[over:]...)
	}
}

// pruneSelfWritesLocked drops every link whose path has seen no write for
// longer than selfWriteTTL. Caller holds selfWrites.mu.
func pruneSelfWritesLocked(now time.Time) {
	newest := make(map[string]time.Time)
	for _, e := range selfWrites.entries {
		if e.at.After(newest[e.path]) {
			newest[e.path] = e.at
		}
	}
	kept := selfWrites.entries[:0]
	for _, e := range selfWrites.entries {
		if now.Sub(newest[e.path]) <= selfWriteTTL {
			kept = append(kept, e)
		}
	}
	clear(selfWrites.entries[len(kept):])
	selfWrites.entries = kept
}

// consumeSelfWriteChain reports whether the file at path moved from baseline
// to current purely through this process's registered writes. On true, the
// links used are consumed (one-shot). On false, every link for path is
// dropped: the watcher is about to reload, and the reloaded generation's
// watcher starts from the post-reload bytes, so any leftover link is stale.
//
// The walk is greedy over links in write order (writes to one path are
// serialized by lockForPath, so registration order is write order) and
// remembers the LAST point at which the chain stood on current, so a cycle
// such as a write followed by its rollback (S→A, A→S) is consumed whole.
// A zero-length chain (baseline == current with no link) is NOT a self
// write: it is a foreign rewrite of identical bytes, and keeps reloading.
func consumeSelfWriteChain(path string, baseline, current [32]byte, now time.Time) bool {
	key := canonicalPath(path)
	selfWrites.mu.Lock()
	defer selfWrites.mu.Unlock()
	pruneSelfWritesLocked(now)
	if selfWrites.overflowed[key] {
		// Links for this path were evicted since the last settle: the chain
		// can no longer prove the file moved only through our writes.
		delete(selfWrites.overflowed, key)
		dropPathLinksLocked(key)
		return false
	}

	cur := baseline
	matchedThrough := -1 // index into entries of the last link that landed on current
	for i, e := range selfWrites.entries {
		if e.path != key {
			continue
		}
		if e.pre != cur {
			break
		}
		cur = e.post
		if cur == current {
			matchedThrough = i
		}
	}

	kept := selfWrites.entries[:0]
	for i, e := range selfWrites.entries {
		if e.path == key && (matchedThrough < 0 || i <= matchedThrough) {
			continue
		}
		kept = append(kept, e)
	}
	clear(selfWrites.entries[len(kept):])
	selfWrites.entries = kept
	return matchedThrough >= 0
}

// forgetSelfWrites drops every link registered for path. The daemon's reload
// step calls it (via ForgetSelfWrites) right after config.Load, holding the
// settings lock every registering writer takes (BH3, M1-close C2): every link
// registered before that load is already reflected in the loaded bytes — the
// watcher's baseline — so it is history, not part of any chain FROM that
// baseline. Left in place, a link whose pre predates the baseline (a save
// that landed in the reload window) sat at the head of the chain walk and
// broke it, turning the next save into a spurious reload. Dropping links can
// only cause a reload, never suppress one, so no operator edit can be
// swallowed by it.
func forgetSelfWrites(path string) {
	key := canonicalPath(path)
	selfWrites.mu.Lock()
	defer selfWrites.mu.Unlock()
	delete(selfWrites.overflowed, key)
	dropPathLinksLocked(key)
}

// dropPathLinksLocked removes every link for key. Caller holds selfWrites.mu.
func dropPathLinksLocked(key string) {
	kept := selfWrites.entries[:0]
	for _, e := range selfWrites.entries {
		if e.path != key {
			kept = append(kept, e)
		}
	}
	clear(selfWrites.entries[len(kept):])
	selfWrites.entries = kept
}

// ForgetSelfWrites is forgetSelfWrites for the daemon's reload step: called
// right after config.Load, while the caller holds the lock every registering
// writer takes, it drops the links the loaded bytes already reflect.
func ForgetSelfWrites(path string) { forgetSelfWrites(path) }

// firstReadingSuspect decides, for WatchFrom's first reading, whether the
// registry shows a foreign write that the hash comparison against the loaded
// bytes cannot see. Conservative rule (M1-close D4/E2/E3): walk the chain of
// this process's writes to path from baseline (the loaded hash), exactly as
// consumeSelfWriteChain does, and report true when
//
//   - any registered link for path is NOT on that chain: a daemon write read
//     bytes the chain never reached, i.e. something else wrote the file
//     first (E2: operator edit X, daemon save over X, operator revert);
//   - the chain exists, the current bytes equal the baseline, and the chain
//     does not end on them (D4: daemon save, then an operator revert to the
//     loaded bytes); or
//   - links for path were evicted by the cap (E3).
//
// A pure save, a save and its own rollback, and a burst of saves on top of
// the baseline are one unbroken chain and report false. A true result makes
// the first reading a pending change; at settle time the chain cannot be
// consumed, so the watcher reloads.
func firstReadingSuspect(path string, baseline, current [32]byte, now time.Time) bool {
	key := canonicalPath(path)
	selfWrites.mu.Lock()
	defer selfWrites.mu.Unlock()
	pruneSelfWritesLocked(now)
	if selfWrites.overflowed[key] {
		return true
	}
	end, onChain, total := baseline, 0, 0
	broken := false
	for _, e := range selfWrites.entries {
		if e.path != key {
			continue
		}
		total++
		if broken || e.pre != end {
			broken = true
			continue
		}
		end = e.post
		onChain++
	}
	if total > onChain {
		return true
	}
	return onChain > 0 && current == baseline && end != current
}
