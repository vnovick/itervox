package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestModelsClientHasTimeout (#187): model discovery uses a client with its
// own timeout, never http.DefaultClient (which has none), and a hung API
// returns the fallback catalog once that timeout passes.
func TestModelsClientHasTimeout(t *testing.T) {
	assert.NotSame(t, http.DefaultClient, modelsHTTPClient, "discovery must not use http.DefaultClient")
	assert.Greater(t, modelsHTTPClient.Timeout, time.Duration(0), "the discovery client needs a timeout")
	assert.LessOrEqual(t, modelsHTTPClient.Timeout, 30*time.Second)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // hang until the test ends or the client gives up
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	orig := modelsHTTPClient
	modelsHTTPClient = &http.Client{Timeout: 200 * time.Millisecond}
	defer func() { modelsHTTPClient = orig }()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("ITERVOX_ANTHROPIC_API_BASE", srv.URL)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ITERVOX_OPENAI_API_BASE", srv.URL)

	start := time.Now()
	assert.Equal(t, DefaultClaudeModels, ListClaudeModels())
	assert.Equal(t, DefaultCodexModels, ListCodexModels())
	assert.Less(t, time.Since(start), 3*time.Second, "the client timeout, not the 10 s context, must end a hung request")
}
