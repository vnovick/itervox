package store

import (
	"context"
	"time"

	"github.com/vnovick/itervox/internal/metrics"
)

// RequestSyncNowForTest runs one pull now, as Run's timer would: incremental
// when one is due (#114), otherwise full.
func (s *Store) RequestSyncNowForTest(ctx context.Context) error {
	return s.sync(ctx, false, metrics.StorePullInterval)
}

// CursorForTest is the incremental-pull cursor (#114).
func (s *Store) CursorForTest() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

// RetryDelayForTest is retryDelay.
var RetryDelayForTest = retryDelay
