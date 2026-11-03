package sysmon

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestMemberIndexesFollowsGroupsAndDescendants: a run's members are its
// groups' members, its leaders and every descendant, including one that
// left the group (setsid); an unrelated process and pgid 0 (Windows) are
// not members.
func TestMemberIndexesFollowsGroupsAndDescendants(t *testing.T) {
	procs := []procInfo{
		{PID: 1, PPID: 0, PGID: 1},
		{PID: 100, PPID: 1, PGID: 100},   // leader
		{PID: 101, PPID: 100, PGID: 100}, // group member
		{PID: 102, PPID: 101, PGID: 102}, // setsid'd grandchild
		{PID: 103, PPID: 102, PGID: 102}, // its child
		{PID: 200, PPID: 1, PGID: 200},   // unrelated
		{PID: 300, PPID: 1, PGID: 0},     // no group (Windows)
	}
	got := memberIndexes(procs, map[int]bool{100: true}, map[int]bool{100: true})
	var pids []int
	for _, i := range got {
		pids = append(pids, procs[i].PID)
	}
	assert.Equal(t, []int{100, 101, 102, 103}, pids)

	assert.Empty(t, memberIndexes(procs, map[int]bool{0: true}, nil), "pgid 0 never matches")
	got = memberIndexes(procs, nil, map[int]bool{300: true})
	assert.Len(t, got, 1, "a leader alone (Windows) is a member")
}

// TestRunSamplerTotalsAreMonotonic: CPU percent is the delta over the
// interval, totals keep the CPU and I/O of processes that exited, and the
// peaks never go down.
func TestRunSamplerTotalsAreMonotonic(t *testing.T) {
	t0 := time.Unix(1000, 0)
	s := newRunSampler(t0)
	var u RunUsage
	a := procInfo{PID: 10, Start: 1, Comm: "node", Argv: []string{"/usr/bin/node", "cli.js"}, CPUSeconds: 1, RSSBytes: 100, ReadBytes: 10, WriteBytes: 20}
	b := procInfo{PID: 11, Start: 1, Comm: "rg", Argv: []string{"rg", "TODO"}, CPUSeconds: 0.5, RSSBytes: 50, ReadBytes: 1, WriteBytes: 2,
		Conns: []NetConn{{Inode: 7, State: "ESTABLISHED"}, {Inode: 8, State: "LISTEN"}}}
	s.observe(t0.Add(2*time.Second), []procInfo{a, b}, true, &u)
	assert.InDelta(t, 75.0, u.CPUPercent, 0.01, "1.5 CPU-seconds over 2s")
	assert.Equal(t, uint64(150), u.RSSBytes)
	assert.Equal(t, 2, u.Processes)
	assert.Equal(t, 1, u.NetConnections, "the listening socket is not a connection")
	assert.Equal(t, "node", u.TopCommands[0].Name, "sorted by CPU")
	assert.Equal(t, "cli.js", u.TopCommands[0].Args)

	// b exits; a uses one more CPU-second and grows.
	a.CPUSeconds, a.RSSBytes, a.WriteBytes = 2, 120, 25
	s.observe(t0.Add(4*time.Second), []procInfo{a}, true, &u)
	assert.InDelta(t, 50.0, u.CPUPercent, 0.01)
	assert.InDelta(t, 2.5, u.CPUSeconds, 0.01, "b's 0.5s is kept")
	assert.Equal(t, uint64(120), u.RSSBytes)
	assert.Equal(t, uint64(150), u.PeakRSSBytes)
	assert.Equal(t, 1, u.Processes)
	assert.Equal(t, 2, u.PeakProcesses)
	assert.Equal(t, uint64(11), u.DiskReadBytes)
	assert.Equal(t, uint64(27), u.DiskWriteBytes)
	assert.Equal(t, 2, u.Samples)
	assert.True(t, u.DiskIOKnown)
}

// TestCommandLineRedaction: credentials in argv never reach a child
// command row or the activity log.
func TestCommandLineRedaction(t *testing.T) {
	secret := "sk-ant-" + strings.Repeat("A1b2C3d4", 6)
	p := procInfo{Comm: "curl", Argv: []string{
		"/usr/bin/curl", "--token=abc123", "--password", "hunter2", "GITHUB_TOKEN=plainvalue",
		"https://alice:pw@example.com/x", "-H", "x-api-key: " + secret, "--verbose",
	}}
	name, args := commandLine(&p)
	assert.Equal(t, "curl", name)
	for _, leaked := range []string{"abc123", "hunter2", "plainvalue", "alice", ":pw@", secret} {
		assert.NotContains(t, args, leaked)
	}
	assert.Contains(t, args, "--token=[redacted]")
	assert.Contains(t, args, "--password [redacted]")
	assert.Contains(t, args, "GITHUB_TOKEN=")
	assert.Contains(t, args, "https://[redacted]@example.com/x")
	assert.Contains(t, args, "--verbose")

	long := procInfo{Argv: []string{"x", strings.Repeat("word ", 100)}}
	_, args = commandLine(&long)
	assert.LessOrEqual(t, len(args), argsMaxBytes)
	assert.True(t, strings.HasSuffix(args, "…"))
}
