package outbox_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const legacyKeylessOutbox = `[{"id":"1-aa","kind":"create_comment","issue_id":"A","identifier":"ENG-1",` +
	`"body":"hello","enqueued_at":"2026-08-06T12:00:00Z","attempts":0,` +
	`"next_attempt_at":"2026-08-06T12:01:00Z"}]`

// TestLegacyEntryKeyStableAcrossReopens (CORE-122): a legacy keyless comment
// entry must get the SAME key on every open with no acknowledgement in
// between — including when the outbox cannot be written, so the load-time
// persist fails and the key never reaches disk. A fresh random key per open
// meant one duplicate comment per crash in the post→persist window.
func TestLegacyEntryKeyStableAcrossReopens(t *testing.T) {
	t.Run("unwritable outbox", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "outbox.json")
		require.NoError(t, os.WriteFile(path, []byte(legacyKeylessOutbox), 0o600))
		require.NoError(t, os.Chmod(dir, 0o500)) // load-time persist fails
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		var keys []string
		for range 3 {
			entries := mustNew(t, path).Snapshot()
			require.Len(t, entries, 1)
			require.NotEmpty(t, entries[0].CommentKey)
			keys = append(keys, entries[0].CommentKey)
		}
		assert.Equal(t, keys[0], keys[1])
		assert.Equal(t, keys[0], keys[2])
		assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, keys[0],
			"Linear uses the key as the comment id, so it must stay a UUID")
	})

	t.Run("writable outbox persists the key at load", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "outbox.json")
		require.NoError(t, os.WriteFile(path, []byte(legacyKeylessOutbox), 0o600))

		key := mustNew(t, path).Snapshot()[0].CommentKey
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Contains(t, string(raw), key, "the backfilled key is durable before the first delivery attempt")
	})
}
