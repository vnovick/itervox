package store

import (
	"context"

	"github.com/vnovick/itervox/internal/metrics"
)

// RequestSyncNowForTest runs one full pull now, as Run's timer would.
func (s *Store) RequestSyncNowForTest(ctx context.Context) error {
	return s.sync(ctx, false, metrics.StorePullInterval)
}
