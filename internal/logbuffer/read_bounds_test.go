package logbuffer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

// M0-close fix-G — reproductions for the milestone-M0 bug hunt's logbuffer
// findings: reads of unknown identifiers growing the Buffer's maps (item 2)
// and empty-window reads that cannot give up on a stalled disk (item 3).

// mapSizes reports how many issueBufs the Buffer holds and how many files the
// disk writer has state for. Call it only after a Flush (or before any disk
// op): the Flush barrier orders the writer's map writes before this read.
func (b *Buffer) mapSizes() (issues, files int) {
	b.issues.Range(func(any, any) bool { issues++; return true })
	return issues, len(b.w.files)
}

// TestRead_UnknownIdentifierDoesNotGrowMaps (item 2): a read of an identifier
// with neither an issueBuf nor a log file returns empty and creates nothing,
// so client-chosen identifiers cannot grow the maps. A real issue keeps its
// issueBuf (CORE-003 seq retention), and an on-disk file with no issueBuf is
// still served.
func TestRead_UnknownIdentifierDoesNotGrowMaps(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	ctx := context.Background()

	b.Add("ENG-1", "L1")
	b.Remove("ENG-1")
	require.NoError(t, b.Flush(ctx))
	issues0, files0 := b.mapSizes()
	require.Equal(t, 1, issues0)
	require.Equal(t, 1, files0)

	ep := b.Epoch()
	for i := range 50 {
		id := fmt.Sprintf("BOGUS-%d", i)
		assert.Empty(t, b.Get(id))
		lines, _, next, gap := b.GetSince(id, ep, 0, false)
		assert.Empty(t, lines)
		assert.Zero(t, next)
		assert.False(t, gap)
		lines, _, _, _ = b.GetSince(id, ep, 7, true)
		assert.Empty(t, lines)
	}
	require.NoError(t, b.Flush(ctx))
	issues, files := b.mapSizes()
	assert.Equal(t, issues0, issues, "reads of unknown identifiers must not create issueBufs")
	assert.Equal(t, files0, files, "reads of unknown identifiers must not create writer file state")

	// The real, Removed issue still serves its file with preserved numbering.
	lines, _, next, _ := b.GetSince("ENG-1", ep, 0, false)
	assert.Equal(t, []string{"L1"}, lines)
	assert.Equal(t, int64(1), next)

	// A file left by an earlier process (no issueBuf yet) is still served.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "OLD-1.log"), []byte("a\nb\n"), 0o644))
	lines, _, next, _ = b.GetSince("OLD-1", ep, 0, false)
	assert.Equal(t, []string{"a", "b"}, lines)
	assert.Equal(t, int64(2), next)
}

// TestLogReadHandlers_ReturnWhenClientGoesAway (item 3): with the disk
// stalled under an empty-window read, cancelling the request's context must
// end both the /logs and the /log-stream handler promptly instead of leaving
// them parked until the disk recovers.
func TestLogReadHandlers_ReturnWhenClientGoesAway(t *testing.T) {
	for _, route := range []string{"logs", "log-stream"} {
		t.Run(route, func(t *testing.T) {
			b := New()
			b.SetLogDir(t.TempDir())
			t.Cleanup(func() { _ = b.Close(context.Background()) })
			b.Add("ENG-1", `{"level":"INFO","msg":"hi"}`)
			b.Remove("ENG-1") // empty window: the next read goes to disk
			require.NoError(t, b.Flush(context.Background()))

			stalled := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			b.beforeDiskRead = func(string) {
				once.Do(func() { close(stalled) })
				<-release
			}

			srv := server.New(server.Config{
				Snapshot:    func() server.StateSnapshot { return server.StateSnapshot{} },
				RefreshChan: make(chan struct{}, 1),
				Client:      logReadClient{b: b},
				// httptest.NewRequest addresses example.com; no token => CORE-162 Host guard.
				AllowedHosts: []string{"example.com"},
			})
			ctx, cancel := context.WithCancel(context.Background())
			req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/"+route, nil).WithContext(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				srv.ServeHTTP(httptest.NewRecorder(), req)
			}()
			t.Cleanup(func() { close(release); cancel(); <-done })

			select {
			case <-stalled:
			case <-time.After(promptly):
				t.Fatal("the handler never reached the disk read")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(promptly):
				t.Fatal("handler still parked on the stalled disk after its client went away")
			}
		})
	}
}

// logReadClient serves the two log reads these routes make from the buffer;
// every other OrchestratorClient method is the nil embedded interface
// (server.FuncClient is test-only in package server, CORE-110).
type logReadClient struct {
	server.OrchestratorClient
	b *Buffer
}

func (c logReadClient) FetchLogs(ctx context.Context, identifier string) []string {
	return c.b.GetContext(ctx, identifier)
}

func (c logReadClient) GetSince(ctx context.Context, identifier string, epoch uint32, cursor int64, hasCursor bool) ([]string, uint32, int64, bool) {
	return c.b.GetSinceContext(ctx, identifier, epoch, cursor, hasCursor)
}
