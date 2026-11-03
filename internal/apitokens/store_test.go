package apitokens

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), ".itervox", FileName))
}

// TestTokensStoredHashedOnly (#214): tokens.json holds the SHA-256 of each
// secret and never the secret, is written 0600, and the secret
// authenticates with the token's scope.
func TestTokensStoredHashedOnly(t *testing.T) {
	s := tempStore(t)
	tok, secret, err := s.Create("ci", ScopeViewer, nil, "test")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(secret, SecretPrefix))
	assert.GreaterOrEqual(t, len(secret), len(SecretPrefix)+43, "256 bits of randomness")

	raw, err := os.ReadFile(s.FilePath())
	require.NoError(t, err)
	assert.NotContains(t, string(raw), secret)
	assert.NotContains(t, string(raw), strings.TrimPrefix(secret, SecretPrefix))
	assert.Contains(t, string(raw), HashSecret(secret))
	assert.Equal(t, HashSecret(secret), tok.Hash)
	assert.Len(t, tok.Hash, 64)

	info, err := os.Stat(s.FilePath())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	got, ok := s.Authenticate(secret)
	require.True(t, ok)
	assert.Equal(t, ScopeViewer, got.Scope)
	_, ok = s.Authenticate(secret + "x")
	assert.False(t, ok)
	_, ok = s.Authenticate(HashSecret(secret))
	assert.False(t, ok, "the stored hash is not a credential")

	_, rotated, err := s.Rotate(tok.ID, "test")
	require.NoError(t, err)
	raw, err = os.ReadFile(s.FilePath())
	require.NoError(t, err)
	assert.NotContains(t, string(raw), rotated)
	assert.NotContains(t, string(raw), HashSecret(secret), "rotation drops the old hash")
}

// TestInvalidStoreFailsClosed: a corrupt or unknown-version tokens.json
// authenticates nothing and is never overwritten by a mutation.
func TestInvalidStoreFailsClosed(t *testing.T) {
	s := tempStore(t)
	_, secret, err := s.Create("ci", ScopeAdmin, nil, "test")
	require.NoError(t, err)
	for _, content := range []string{"{not json", `{"version":2,"tokens":[]}`, `{"version":1,"tokens":[{"id":"a","scope":"root","hash":"00"}]}`} {
		require.NoError(t, os.WriteFile(s.FilePath(), []byte(content), 0o600))
		reader := Open(s.FilePath())
		_, ok := reader.Authenticate(secret)
		assert.False(t, ok, content)
		_, err := reader.List()
		assert.Error(t, err, content)
		_, _, err = reader.Create("x", ScopeViewer, nil, "test")
		require.Error(t, err, content)
		after, _ := os.ReadFile(s.FilePath())
		assert.Equal(t, content, string(after), "a mutation never writes over an invalid store")
	}
}

// TestExpiredTokenRefused: a token past its expiry is refused.
func TestExpiredTokenRefused(t *testing.T) {
	s := tempStore(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	exp := now.Add(time.Hour)
	_, secret, err := s.Create("short", ScopeOperator, &exp, "test")
	require.NoError(t, err)
	_, ok := s.Authenticate(secret)
	require.True(t, ok)
	now = now.Add(2 * time.Hour)
	_, ok = s.Authenticate(secret)
	assert.False(t, ok)
	past := now.Add(-time.Minute)
	_, _, err = s.Create("late", ScopeViewer, &past, "test")
	assert.ErrorIs(t, err, ErrExpiresInPast)
}

// TestFlushLastUsedNeverResurrectsARevokedToken: the daemon's lastUsed
// write starts from the file as it is now, so a revoke by another process
// in between stays revoked.
func TestFlushLastUsedNeverResurrectsARevokedToken(t *testing.T) {
	daemon := tempStore(t)
	tok, secret, err := daemon.Create("ci", ScopeViewer, nil, "test")
	require.NoError(t, err)
	_, ok := daemon.Authenticate(secret)
	require.True(t, ok)

	cli := Open(daemon.FilePath())
	_, err = cli.Revoke(tok.ID, "cli")
	require.NoError(t, err)
	require.NoError(t, daemon.FlushLastUsed())

	list, err := Open(daemon.FilePath()).List()
	require.NoError(t, err)
	assert.Empty(t, list)

	tok2, secret2, err := daemon.Create("ci2", ScopeViewer, nil, "test")
	require.NoError(t, err)
	_, ok = daemon.Authenticate(secret2)
	require.True(t, ok)
	require.NoError(t, daemon.FlushLastUsed())
	list, err = Open(daemon.FilePath()).List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, tok2.ID, list[0].ID)
	assert.NotNil(t, list[0].LastUsed)
}

func TestScopeOrdering(t *testing.T) {
	assert.True(t, ScopeAdmin.Allows(ScopeOperator))
	assert.True(t, ScopeOperator.Allows(ScopeViewer))
	assert.False(t, ScopeViewer.Allows(ScopeOperator))
	assert.False(t, ScopeOperator.Allows(ScopeAdmin))
	assert.False(t, Scope("").Allows(ScopeViewer))
	assert.False(t, ScopeAdmin.Allows(Scope("public")))
	sc, err := ParseScope(" Operator ")
	require.NoError(t, err)
	assert.Equal(t, ScopeOperator, sc)
	_, err = ParseScope("root")
	assert.Error(t, err)
}

func TestParseExpiry(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	got, err := ParseExpiry("90d", now)
	require.NoError(t, err)
	assert.Equal(t, now.Add(90*24*time.Hour), *got)
	got, err = ParseExpiry("12h", now)
	require.NoError(t, err)
	assert.Equal(t, now.Add(12*time.Hour), *got)
	got, err = ParseExpiry("2027-01-31", now)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC), *got)
	got, err = ParseExpiry("", now)
	require.NoError(t, err)
	assert.Nil(t, got)
	_, err = ParseExpiry("2020-01-01", now)
	assert.ErrorIs(t, err, ErrExpiresInPast)
	_, err = ParseExpiry("soon", now)
	assert.Error(t, err)
}
