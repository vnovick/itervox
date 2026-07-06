package server

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type deadlineResponseWriter struct {
	header    http.Header
	body      bytes.Buffer
	deadlines []time.Time
}

func (w *deadlineResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *deadlineResponseWriter) Write(b []byte) (int, error) {
	return w.body.Write(b)
}

func (w *deadlineResponseWriter) WriteHeader(int) {}

func (w *deadlineResponseWriter) Flush() {}

func (w *deadlineResponseWriter) SetWriteDeadline(t time.Time) error {
	w.deadlines = append(w.deadlines, t)
	return nil
}

func TestSSEWriteHelpersSetDeadlineForFrameAndFlush(t *testing.T) {
	w := &deadlineResponseWriter{}

	require.NoError(t, writeSSEFrame(w, "event: log\ndata: %s\n\n", "hello"))
	flushSSE(w, w)

	require.Equal(t, "event: log\ndata: hello\n\n", w.body.String())
	require.Len(t, w.deadlines, 2)
	require.True(t, w.deadlines[0].After(time.Now()))
	require.True(t, w.deadlines[1].After(time.Now()))
}
