package logbuffer

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func subagentLine(i int) string {
	return fmt.Sprintf(`time=t level=INFO msg="claude: subagent" n=%d`, i)
}

func countMarkers(lines []string) int {
	n := 0
	for _, l := range lines {
		if IsSubagentMarker(l) {
			n++
		}
	}
	return n
}

// CORE-035: SubagentCount is maintained incrementally on Add and keeps the
// old recount's meaning — marker lines in the RETAINED ring — so it goes
// down when marker lines age out of the 500-line window, and to 0 on
// Remove/Clear.
func TestLogBufferSubagentCountTracksRing(t *testing.T) {
	b := New()
	const id = "ENG-1"
	for i := 0; i < 10; i++ {
		b.Add(id, subagentLine(i))
	}
	b.Add(id, `time=t level=INFO msg="codex: subagent"`)
	b.Add(id, `time=t level=INFO msg="claude: text"`)
	require.Equal(t, 11, b.SubagentCount(id))

	// 12 lines + 495 plain = 507: the 500-line ring drops the first 7 lines,
	// all markers, so 4 of the 11 markers remain.
	for i := 0; i < 495; i++ {
		b.Add(id, "plain line")
	}
	assert.Equal(t, countMarkers(b.Get(id)), b.SubagentCount(id), "count must equal markers retained in the ring")
	assert.Equal(t, 4, b.SubagentCount(id))

	for i := 0; i < 500; i++ {
		b.Add(id, "plain line")
	}
	assert.Equal(t, 0, b.SubagentCount(id), "every marker aged out of the ring")

	b.Add(id, subagentLine(1))
	assert.Equal(t, 1, b.SubagentCount(id))
	b.Remove(id)
	assert.Equal(t, 0, b.SubagentCount(id), "Remove drops the counter with the window")
	assert.Equal(t, 0, b.SubagentCount("never-seen"))
}

// CORE-035: concurrent Add of marker and non-marker lines from several
// goroutines, with interleaved Get and SubagentCount, under -race. The final
// count equals the markers present in the retained ring, and is 0 after
// Clear.
func TestLogBufferSubagentCountConcurrentAddGet(t *testing.T) {
	b := New()
	const id = "ENG-1"
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				if (i+g)%3 == 0 {
					b.Add(id, subagentLine(i))
				} else {
					b.Add(id, fmt.Sprintf("plain %d/%d", g, i))
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			lines := b.Get(id)
			assert.LessOrEqual(t, len(lines), maxLinesPerIssue)
			c := b.SubagentCount(id)
			assert.GreaterOrEqual(t, c, 0)
			assert.LessOrEqual(t, c, maxLinesPerIssue)
		}
	}()
	wg.Wait()

	lines := b.Get(id)
	require.Len(t, lines, maxLinesPerIssue)
	assert.Equal(t, countMarkers(lines), b.SubagentCount(id))

	require.NoError(t, b.Clear(id))
	assert.Equal(t, 0, b.SubagentCount(id), "Clear drops the counter")
	b.Add(id, subagentLine(0))
	require.NoError(t, b.ClearAll())
	assert.Equal(t, 0, b.SubagentCount(id), "ClearAll drops the counter")
}

func TestIsSubagentMarker(t *testing.T) {
	assert.True(t, IsSubagentMarker(`msg="claude: subagent" tool=Task`))
	assert.True(t, IsSubagentMarker(`msg="codex: subagent"`))
	assert.False(t, IsSubagentMarker(`msg="claude: text" text="claude: subagent"`[:20]))
	assert.False(t, IsSubagentMarker(strings.Repeat("x", 10)))
}
