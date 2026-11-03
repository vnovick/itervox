package extsessions

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// TestExtSessionsSkipsLsofForKnownPIDs pins the per-process lsof cache of
// the ps/lsof scan (#212): a process already inspected and still running
// with the same start time is not inspected again until lsofCacheTTL; a
// reused pid (new start time), a new process and an expired entry are; a
// process without a cwd is never cached; gone processes are forgotten.
func TestExtSessionsSkipsLsofForKnownPIDs(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	ps := ""
	list := func() ([]byte, error) { return []byte(ps), nil }
	var calls []int
	lsof := func(pid int) (string, []string) {
		calls = append(calls, pid)
		if pid == 13 {
			return "", nil // lsof could not read it
		}
		return fmt.Sprintf("/work/%d", pid), []string{fmt.Sprintf("/t/%d.jsonl", pid)}
	}
	var cache lsofCache
	scan := func(now time.Time) []Process {
		t.Helper()
		calls = nil
		procs, err := inspectPS(list, lsof, &cache, now)
		if err != nil {
			t.Fatal(err)
		}
		return procs
	}

	// claude 11 and codex 12 running for 10 s, 13 unreadable, 20 not an agent.
	ps = "11 1 00:10 /usr/local/bin/claude\n12 1 00:10 codex\n13 1 00:10 claude\n20 1 00:10 /bin/zsh\n"
	procs := scan(t0)
	if !slices.Equal(calls, []int{11, 12, 13}) {
		t.Fatalf("first scan lsof calls = %v, want every agent", calls)
	}
	if procs[0].Cwd != "/work/11" || !slices.Equal(procs[0].Files, []string{"/t/11.jsonl"}) {
		t.Fatalf("claude 11 = %+v", procs[0])
	}

	// 15 s later the same processes (etime +15 s): only the unreadable one.
	ps = "11 1 00:25 /usr/local/bin/claude\n12 1 00:25 codex\n13 1 00:25 claude\n20 1 00:25 /bin/zsh\n"
	procs = scan(t0.Add(15 * time.Second))
	if !slices.Equal(calls, []int{13}) {
		t.Fatalf("second scan lsof calls = %v, want only the uncached pid 13", calls)
	}
	if procs[0].Cwd != "/work/11" || !slices.Equal(procs[0].Files, []string{"/t/11.jsonl"}) || procs[1].Cwd != "/work/12" {
		t.Fatalf("cached results not applied: %+v", procs)
	}

	// pid 12 now belongs to a new codex started 3 s ago; 14 is new.
	ps = "11 1 00:40 /usr/local/bin/claude\n12 1 00:03 codex\n14 1 00:01 claude\n"
	scan(t0.Add(30 * time.Second))
	if !slices.Equal(calls, []int{12, 14}) {
		t.Fatalf("third scan lsof calls = %v, want the reused pid 12 and the new 14", calls)
	}

	// Past the TTL every agent is inspected again.
	ps = "11 1 02:40 /usr/local/bin/claude\n"
	scan(t0.Add(30*time.Second + lsofCacheTTL))
	if !slices.Equal(calls, []int{11}) {
		t.Fatalf("after the TTL lsof calls = %v, want 11", calls)
	}
	if _, ok := cache.entries[12]; ok || len(cache.entries) != 1 {
		t.Fatalf("processes no longer listed stay cached: %v", cache.entries)
	}

	// An unreadable etime is never cached.
	ps = "11 1 ?? /usr/local/bin/claude\n"
	scan(t0.Add(31*time.Second + lsofCacheTTL))
	scan(t0.Add(32*time.Second + lsofCacheTTL))
	if !slices.Equal(calls, []int{11}) {
		t.Fatalf("unknown start time: lsof calls = %v, want 11 every scan", calls)
	}

	// Without lsof nothing is inspected and nothing breaks.
	procs, err := inspectPS(list, nil, &cache, t0)
	if err != nil || procs[0].Cwd != "" {
		t.Fatalf("no lsof: %+v %v", procs, err)
	}
}
