package sysmon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestActivityLogDedupesAndCaps: one record per distinct command line,
// path or remote, with a count; beyond a cap new distinct records are
// counted as dropped, while known ones still count.
func TestActivityLogDedupesAndCaps(t *testing.T) {
	a := newActivityLog()
	at := time.Unix(100, 0)
	a.addCommand(at, 1, "git", "status")
	a.addCommand(at.Add(time.Second), 2, "git", "status")
	a.addCommand(at, 3, "git", "diff")
	a.addFile(at, "/ws/a.go", false, "node")
	a.addFile(at, "/ws/a.go", true, "node")
	a.addConn(at, "tcp", "1.2.3.4:443", "ESTABLISHED", "node")
	a.addConn(at, "tcp", "1.2.3.4:443", "CLOSE_WAIT", "node")
	require.Len(t, a.commands, 2)
	assert.Equal(t, 2, a.commands[0].Count)
	assert.Equal(t, 2, a.commands[0].PID, "the newest pid")
	require.Len(t, a.files, 1)
	assert.True(t, a.files[0].Write, "a write upgrades a read")
	require.Len(t, a.conns, 1)
	assert.Equal(t, "CLOSE_WAIT", a.conns[0].State)

	for i := range maxActivityCommands + 5 {
		a.addCommand(at, i, "cmd", fmt.Sprint(i))
	}
	assert.Len(t, a.commands, maxActivityCommands)
	assert.Equal(t, 7, a.dropped.Commands, "2 known + 498 new fit; 7 distinct ones did not")
	a.addCommand(at, 9, "git", "status")
	assert.Equal(t, 3, a.commands[0].Count, "a known command still counts at the cap")

	for i := range maxActivityFiles + 1 {
		a.addFile(at, fmt.Sprintf("/f/%d", i), false, "x")
	}
	assert.Len(t, a.files, maxActivityFiles)
	assert.Equal(t, 2, a.dropped.Files)
	for i := range maxActivityConnections + 1 {
		a.addConn(at, "tcp", fmt.Sprintf("10.0.0.1:%d", i+1), "", "x")
	}
	assert.Len(t, a.conns, maxActivityConnections)
	assert.Equal(t, 2, a.dropped.Connections)

	var r ActivityReport
	a.fill(&r, "/ws")
	assert.False(t, r.Files[0].OutsideWorkspace, "/ws/a.go is inside")
	assert.False(t, r.Files[1].OutsideWorkspace, "/f/0 is outside, but it was only read")
}

func TestOutsideWorkspaceOnlyFlagsWrites(t *testing.T) {
	a := newActivityLog()
	at := time.Unix(1, 0)
	a.addFile(at, "/ws/sub/x", true, "sh")
	a.addFile(at, "/ws-other/y", true, "sh")
	a.addFile(at, "/etc/hosts", false, "sh")
	var r ActivityReport
	a.fill(&r, "/ws")
	assert.False(t, r.Files[0].OutsideWorkspace)
	assert.True(t, r.Files[1].OutsideWorkspace, "a sibling with the workspace as a prefix is outside")
	assert.False(t, r.Files[2].OutsideWorkspace, "a read is never flagged")
	assert.True(t, insideDir("/ws", "/ws"))
	assert.False(t, insideDir("/ws", "/w"))
}

// TestActivityStoreRotatesAndRefusesUnknownRuns: the store keeps at most
// maxRuns runs (and maxBytes), removes run files the index does not name,
// and loads only indexed run IDs of the asked identifier.
func TestActivityStoreRotatesAndRefusesUnknownRuns(t *testing.T) {
	dir := t.TempDir()
	s := newActivityStore(dir)
	s.maxRuns = 3
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stray.json"), []byte("{}"), 0o600))
	base := time.Unix(1000, 0)
	for i := range 5 {
		require.NoError(t, s.save(ActivityReport{Identifier: "ENG-1", RunID: fmt.Sprintf("run-%d", i), StartedAt: base.Add(time.Duration(i) * time.Minute)}))
	}
	runs := s.runs("ENG-1")
	require.Len(t, runs, 3)
	assert.Equal(t, "run-4", runs[0].RunID, "newest first")
	_, err := os.Stat(filepath.Join(dir, "run-0.json"))
	assert.True(t, os.IsNotExist(err), "the oldest run was rotated out")
	_, err = os.Stat(filepath.Join(dir, "stray.json"))
	assert.True(t, os.IsNotExist(err), "an unindexed file is removed")

	r, err := s.load("ENG-1", "run-3")
	require.NoError(t, err)
	assert.Equal(t, "run-3", r.RunID)
	_, err = s.load("ENG-2", "run-3")
	assert.ErrorIs(t, err, ErrActivityNotFound, "another issue's run")
	_, err = s.load("ENG-1", "../../etc/passwd")
	assert.ErrorIs(t, err, ErrActivityNotFound)

	// A fresh store reads the index back.
	again := newActivityStore(dir)
	assert.Len(t, again.runs("ENG-1"), 3)

	// The byte cap rotates too.
	s.maxBytes = 1
	require.NoError(t, s.save(ActivityReport{Identifier: "ENG-1", RunID: "run-5", StartedAt: base.Add(time.Hour)}))
	assert.Len(t, s.runs("ENG-1"), 1, "only the newest survives a 1-byte cap")

	assert.NoError(t, s.save(ActivityReport{Identifier: "ENG-1", RunID: "bad/id"}), "a run ID that cannot name a file is not saved")
	assert.Len(t, s.runs("ENG-1"), 1)
}

func TestPlatformNotesSayWhatIsMissing(t *testing.T) {
	sup, notes := platformNotes("darwin", ModeSampled, false)
	assert.False(t, sup.Files)
	assert.Contains(t, notes[len(notes)-1], "Endpoint Security")
	sup, notes = platformNotes("windows", ModeSampled, true)
	assert.False(t, sup.Files)
	assert.Contains(t, notes[0], "SSH worker host")
	sup, notes = platformNotes("linux", ModeSampled, false)
	assert.True(t, sup.Files)
	assert.Contains(t, notes[0], "may be missed")
	sup, _ = platformNotes("linux", ModeOff, false)
	assert.False(t, sup.Commands)
	_, notes = platformNotes("linux", ModePrecise, false)
	assert.Contains(t, notes[0], "eBPF")
}
