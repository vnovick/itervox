package server

// ResetDefaultClientErrorLimiterForTest gives the process-wide client-error
// limiter a fresh state. Tests only.
func ResetDefaultClientErrorLimiterForTest() {
	fresh := NewClientErrorLimiter()
	DefaultClientErrorLimiter.mu.Lock()
	DefaultClientErrorLimiter.clients = fresh.clients
	DefaultClientErrorLimiter.global = tokenBucket{}
	DefaultClientErrorLimiter.mu.Unlock()
}
