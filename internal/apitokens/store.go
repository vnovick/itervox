package apitokens

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// FileName is the store's file name inside the project's .itervox directory.
const FileName = "tokens.json"

// fileVersion is the tokens.json schema version this build reads and writes.
const fileVersion = 1

// SecretPrefix starts every stored-token secret, so a leaked one is easy to
// recognise (and to redact from logs).
const SecretPrefix = "itx_"

// maxNameLen bounds a token name.
const maxNameLen = 64

// lastUsedResolution is how stale a token's persisted lastUsed may get
// before an authentication marks it for the next FlushLastUsed. It keeps the
// daemon from rewriting tokens.json on every request.
const lastUsedResolution = time.Minute

// defaultCheckInterval is how often Authenticate stats tokens.json for a
// change made by another process (`itervox token ...`). A revoke or rotate
// from the CLI therefore takes effect within this interval plus one read;
// it must stay well under the 1 s the docs promise.
const defaultCheckInterval = 200 * time.Millisecond

// lockTimeout bounds how long a mutation waits for the cross-process lock.
const lockTimeout = 5 * time.Second

// Errors returned by Store mutations.
var (
	// ErrNotFound: no token with that id.
	ErrNotFound = errors.New("apitokens: no token with that id")
	// ErrInvalidName: the name is empty, too long or has control characters.
	ErrInvalidName = errors.New("apitokens: name must be 1-64 printable characters")
	// ErrExpiresInPast: the expiry is not in the future.
	ErrExpiresInPast = errors.New("apitokens: expiry must be in the future")
)

// Token is one stored token. Hash is the hex SHA-256 of the secret; the
// secret itself is never stored.
type Token struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Scope    Scope      `json:"scope"`
	Hash     string     `json:"hash"`
	Created  time.Time  `json:"created"`
	LastUsed *time.Time `json:"lastUsed,omitempty"`
	Expires  *time.Time `json:"expires,omitempty"`
}

// Expired reports whether t has an expiry at or before now.
func (t Token) Expired(now time.Time) bool {
	return t.Expires != nil && !now.Before(*t.Expires)
}

// file is the on-disk shape of tokens.json.
type file struct {
	Version int     `json:"version"`
	Tokens  []Token `json:"tokens"`
}

// Path returns the store path for the project whose WORKFLOW.md is
// workflowPath: <dir>/.itervox/tokens.json.
func Path(workflowPath string) string {
	return filepath.Join(filepath.Dir(workflowPath), ".itervox", FileName)
}

// HashSecret returns the hex SHA-256 of secret. Secrets are 256-bit random
// values, so a fast unsalted hash is enough: there is nothing to brute-force.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// fileIdentity is what Authenticate compares to notice a change by another
// process. Atomic writes rename a new inode into place, so os.SameFile alone
// catches every write; modtime and size are a second signal.
type fileIdentity struct {
	info    fs.FileInfo // nil when the file does not exist
	modTime time.Time
	size    int64
}

func (a fileIdentity) same(b fileIdentity) bool {
	if a.info == nil || b.info == nil {
		return a.info == nil && b.info == nil
	}
	return os.SameFile(a.info, b.info) && a.modTime.Equal(b.modTime) && a.size == b.size
}

// Store is the token store for one tokens.json. Authenticate serves from an
// in-memory index that a mutation through this Store refreshes at once and a
// write by another process refreshes within checkInterval (an mtime/inode
// check). It is safe for concurrent use.
type Store struct {
	path          string
	checkInterval time.Duration
	now           func() time.Time

	// writeMu serializes this process's mutations; the flock serializes
	// them against other processes.
	writeMu sync.Mutex

	mu        sync.Mutex
	tokens    []Token
	hashes    [][sha256.Size]byte
	identity  fileIdentity
	loaded    bool
	lastCheck time.Time
	loadErr   error
	// used holds lastUsed times not yet written to the file, by token id.
	used map[string]time.Time
}

// Open returns the store backed by path. Nothing is read until first use; a
// missing file is an empty store.
func Open(path string) *Store {
	return &Store{
		path:          path,
		checkInterval: defaultCheckInterval,
		now:           time.Now,
		used:          make(map[string]time.Time),
	}
}

// FilePath returns the store's file path.
func (s *Store) FilePath() string { return s.path }

// stat returns the current identity of the store file.
func (s *Store) stat() (fileIdentity, error) {
	info, err := os.Stat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileIdentity{}, nil
	}
	if err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{info: info, modTime: info.ModTime(), size: info.Size()}, nil
}

// readFile reads and validates tokens.json. A missing file is an empty
// store. Any invalid content is an error: the caller then accepts no stored
// token at all (fail closed) and refuses to write over the file.
func readFile(path string) ([]Token, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("apitokens: read %s: %w", path, err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("apitokens: %s is not valid JSON: %w", path, err)
	}
	if f.Version != fileVersion {
		return nil, fmt.Errorf("apitokens: %s has version %d; this build reads version %d", path, f.Version, fileVersion)
	}
	seen := make(map[string]struct{}, len(f.Tokens))
	for i, t := range f.Tokens {
		if t.ID == "" || !t.Scope.Valid() || !validHash(t.Hash) {
			return nil, fmt.Errorf("apitokens: %s: token %d is invalid (id, scope and a 64-hex hash are required)", path, i)
		}
		if _, dup := seen[t.ID]; dup {
			return nil, fmt.Errorf("apitokens: %s: duplicate token id %q", path, t.ID)
		}
		seen[t.ID] = struct{}{}
	}
	return f.Tokens, nil
}

func validHash(h string) bool {
	if len(h) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil && strings.ToLower(h) == h
}

// setIndexLocked replaces the in-memory index. s.mu must be held.
func (s *Store) setIndexLocked(tokens []Token, id fileIdentity, loadErr error) {
	s.tokens = tokens
	s.hashes = make([][sha256.Size]byte, len(tokens))
	for i, t := range tokens {
		raw, _ := hex.DecodeString(t.Hash) // validated by readFile / built by us
		copy(s.hashes[i][:], raw)
	}
	s.identity = id
	s.loaded = true
	s.loadErr = loadErr
	// Forget pending lastUsed marks of tokens that no longer exist.
	for tid := range s.used {
		if !slices.ContainsFunc(tokens, func(t Token) bool { return t.ID == tid }) {
			delete(s.used, tid)
		}
	}
}

// refreshLocked reloads the index when the file changed, checking at most
// once per checkInterval unless force is set. s.mu must be held.
func (s *Store) refreshLocked(force bool) {
	now := s.now()
	if s.loaded && !force && now.Sub(s.lastCheck) < s.checkInterval {
		return
	}
	s.lastCheck = now
	id, err := s.stat()
	if err != nil {
		if s.loadErr == nil {
			slog.Error("apitokens: cannot stat the token store; stored tokens are refused until it is readable",
				"path", s.path, "error", err)
		}
		s.setIndexLocked(nil, fileIdentity{}, err)
		return
	}
	if s.loaded && s.identity.same(id) {
		return
	}
	tokens, err := readFile(s.path)
	if err != nil {
		slog.Error("apitokens: token store is invalid; stored tokens are refused until it is fixed",
			"path", s.path, "error", err)
		s.setIndexLocked(nil, id, err)
		return
	}
	if id.info != nil && id.info.Mode().Perm()&0o077 != 0 {
		if chErr := os.Chmod(s.path, 0o600); chErr != nil {
			slog.Warn("apitokens: token store is readable by other users and cannot be tightened",
				"path", s.path, "mode", id.info.Mode().Perm().String(), "error", chErr)
		}
	}
	s.setIndexLocked(tokens, id, nil)
}

// Authenticate returns the stored token whose secret is secret. Unknown,
// expired and malformed secrets, and every secret while the file is invalid,
// are refused. The hash comparison runs over every stored token in constant
// time per entry.
func (s *Store) Authenticate(secret string) (Token, bool) {
	if !strings.HasPrefix(secret, SecretPrefix) {
		return Token{}, false
	}
	sum := sha256.Sum256([]byte(secret))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked(false)
	if s.loadErr != nil {
		return Token{}, false
	}
	match := -1
	for i := range s.hashes {
		if constantTimeEqual(sum[:], s.hashes[i][:]) {
			match = i
		}
	}
	if match < 0 {
		return Token{}, false
	}
	t := s.tokens[match]
	now := s.now()
	if t.Expired(now) {
		return Token{}, false
	}
	if t.LastUsed == nil || now.Sub(*t.LastUsed) >= lastUsedResolution {
		s.used[t.ID] = now
	}
	return t, true
}

// List returns the stored tokens, newest first, with lastUsed times not yet
// flushed merged in. It reads the file afresh.
func (s *Store) List() ([]Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked(true)
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	out := slices.Clone(s.tokens)
	for i := range out {
		if at, ok := s.used[out[i].ID]; ok {
			out[i].LastUsed = &at
		}
	}
	slices.SortStableFunc(out, func(a, b Token) int { return b.Created.Compare(a.Created) })
	return out, nil
}

// mutate runs fn over the current file content under both locks and writes
// the result. It never writes over an invalid file.
func (s *Store) mutate(fn func(tokens []Token) ([]Token, error)) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("apitokens: create %s: %w", filepath.Dir(s.path), err)
	}
	unlock, err := lockFile(s.path+".lock", s.now().Add(lockTimeout))
	if err != nil {
		return err
	}
	defer unlock()
	tokens, err := readFile(s.path)
	if err != nil {
		return err
	}
	next, err := fn(slices.Clone(tokens))
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(file{Version: fileVersion, Tokens: nonNil(next)}, "", "  ")
	if err != nil {
		return fmt.Errorf("apitokens: encode: %w", err)
	}
	if err := atomicfs.WriteFile(s.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("apitokens: write %s: %w", s.path, err)
	}
	id, statErr := s.stat()
	s.mu.Lock()
	defer s.mu.Unlock()
	if statErr != nil {
		// Written but not statable: force a reload on next use.
		s.loaded = false
		return nil
	}
	s.setIndexLocked(next, id, nil)
	s.lastCheck = s.now()
	return nil
}

func nonNil(t []Token) []Token {
	if t == nil {
		return []Token{}
	}
	return t
}

// Create adds a token and returns it with its secret. The secret is
// returned exactly once and never stored. actor names who asked, for the
// audit log line.
func (s *Store) Create(name string, scope Scope, expires *time.Time, actor string) (Token, string, error) {
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return Token{}, "", err
	}
	if !scope.Valid() {
		return Token{}, "", fmt.Errorf("apitokens: unknown scope %q", scope)
	}
	now := s.now().UTC()
	if expires != nil && !expires.After(now) {
		return Token{}, "", ErrExpiresInPast
	}
	secret, err := newSecret()
	if err != nil {
		return Token{}, "", err
	}
	id, err := newID()
	if err != nil {
		return Token{}, "", err
	}
	t := Token{ID: id, Name: name, Scope: scope, Hash: HashSecret(secret), Created: now}
	if expires != nil {
		e := expires.UTC()
		t.Expires = &e
	}
	if err := s.mutate(func(tokens []Token) ([]Token, error) {
		return append(tokens, t), nil
	}); err != nil {
		return Token{}, "", err
	}
	slog.Info("apitokens: token created", "id", t.ID, "name", t.Name, "scope", string(t.Scope),
		"expires", expiryAttr(t.Expires), "actor", actor)
	return t, secret, nil
}

// Revoke deletes the token with id. The old secret stops authenticating in
// this process at once and in a daemon process within its check interval.
func (s *Store) Revoke(id, actor string) (Token, error) {
	var removed Token
	err := s.mutate(func(tokens []Token) ([]Token, error) {
		i := slices.IndexFunc(tokens, func(t Token) bool { return t.ID == id })
		if i < 0 {
			return nil, ErrNotFound
		}
		removed = tokens[i]
		return slices.Delete(tokens, i, i+1), nil
	})
	if err != nil {
		return Token{}, err
	}
	slog.Info("apitokens: token revoked", "id", removed.ID, "name", removed.Name, "scope", string(removed.Scope), "actor", actor)
	return removed, nil
}

// Rotate gives the token with id a new secret, keeping its id, name, scope
// and expiry, and returns the new secret once. The old secret stops
// authenticating as Revoke describes.
func (s *Store) Rotate(id, actor string) (Token, string, error) {
	secret, err := newSecret()
	if err != nil {
		return Token{}, "", err
	}
	var rotated Token
	err = s.mutate(func(tokens []Token) ([]Token, error) {
		i := slices.IndexFunc(tokens, func(t Token) bool { return t.ID == id })
		if i < 0 {
			return nil, ErrNotFound
		}
		tokens[i].Hash = HashSecret(secret)
		tokens[i].LastUsed = nil
		rotated = tokens[i]
		return tokens, nil
	})
	if err != nil {
		return Token{}, "", err
	}
	slog.Info("apitokens: token rotated", "id", rotated.ID, "name", rotated.Name, "scope", string(rotated.Scope), "actor", actor)
	return rotated, secret, nil
}

// FlushLastUsed writes pending lastUsed times to the file. It is a no-op
// when nothing is pending. Tokens revoked meanwhile are not resurrected:
// the write starts from the file as it is now.
func (s *Store) FlushLastUsed() error {
	s.mu.Lock()
	pending := make(map[string]time.Time, len(s.used))
	for id, at := range s.used {
		pending[id] = at
	}
	s.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	err := s.mutate(func(tokens []Token) ([]Token, error) {
		for i := range tokens {
			if at, ok := pending[tokens[i].ID]; ok && (tokens[i].LastUsed == nil || at.After(*tokens[i].LastUsed)) {
				u := at.UTC()
				tokens[i].LastUsed = &u
			}
		}
		return tokens, nil
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	for id, at := range pending {
		if cur, ok := s.used[id]; ok && !cur.After(at) {
			delete(s.used, id)
		}
	}
	s.mu.Unlock()
	return nil
}

func validateName(name string) error {
	if name == "" || len(name) > maxNameLen {
		return ErrInvalidName
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return ErrInvalidName
		}
	}
	return nil
}

func newSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("apitokens: generate secret: %w", err)
	}
	return SecretPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

func newID() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("apitokens: generate id: %w", err)
	}
	return "tok_" + hex.EncodeToString(buf), nil
}

func expiryAttr(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.UTC().Format(time.RFC3339)
}
