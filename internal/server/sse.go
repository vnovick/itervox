package server

import "net/http"

// SSE response setup shared by every streaming handler (CORE-111). The two
// halves stay separate because the handlers order them differently (and the
// order decides which headers a 500 "streaming unsupported" carries).

// setSSEHeaders sets the Server-Sent Events response headers.
func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

// sseFlusher returns w's http.Flusher, or answers 500 "streaming
// unsupported" and reports false when w cannot stream.
func sseFlusher(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return nil, false
	}
	return flusher, true
}

// beginSSE is sseFlusher followed by setSSEHeaders — the usual order.
func beginSSE(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := sseFlusher(w)
	if ok {
		setSSEHeaders(w)
	}
	return flusher, ok
}
