package memory

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// UserMemoryEnv overrides the private memory file's path; "off" disables
// private memory.
const UserMemoryEnv = "ITERVOX_USER_MEMORY"

// UserPath is the private memory file: $ITERVOX_USER_MEMORY, else
// ~/.itervox/memory.json. On a remote daemon that is the daemon user's home,
// the same user-scoped storage as the user rules layer. Empty when private
// memory is off or no home directory is known. Under `go test` it is off
// unless the variable names a file, so a developer's own memory never
// reaches the tests.
func UserPath() string {
	if p := os.Getenv(UserMemoryEnv); p != "" {
		if p == "off" {
			return ""
		}
		return p
	}
	if testing.Testing() {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".itervox", "memory.json")
}

// ProjectPath is the committed project memory file for a workflow; empty
// without a workflow path.
func ProjectPath(workflowPath string) string {
	if workflowPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(workflowPath), ".itervox", "memory.json")
}

// PathFor returns the file of a scope, "" when the scope has none.
func PathFor(workflowPath string, scope Scope) string {
	switch scope {
	case ScopePrivate:
		return UserPath()
	case ScopeProject:
		return ProjectPath(workflowPath)
	}
	return ""
}

// filePerm is the mode a scope's file is written with: the private file is
// the user's own.
func filePerm(scope Scope) os.FileMode {
	if scope == ScopePrivate {
		return 0o600
	}
	return 0o644
}

// File is one memory file as read from disk.
type File struct {
	Scope Scope
	// Path is the file; empty when the scope has none.
	Path string
	// Exists is false when the file does not exist (no entries).
	Exists bool
	// Entries are the parsed entries; nil when Err is set.
	Entries []Entry
	// Err is why the file could not be read or parsed. Such a file
	// contributes no entries, and the daemon refuses to rewrite it.
	Err error
}

// ReadFile reads one scope's file.
func ReadFile(scope Scope, path string) File {
	f := File{Scope: scope, Path: path}
	if path == "" {
		return f
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f
	}
	if err != nil {
		f.Err = fmt.Errorf("memory: read %s: %w", path, err)
		return f
	}
	f.Exists = true
	entries, err := Parse(raw, scope)
	if err != nil {
		f.Err = fmt.Errorf("memory: %s: %w", path, err)
		return f
	}
	f.Entries = entries
	return f
}

// ReadAll reads both files, private first. It never fails as a whole.
func ReadAll(workflowPath string) []File {
	out := make([]File, 0, len(Scopes))
	for _, s := range Scopes {
		out = append(out, ReadFile(s, PathFor(workflowPath, s)))
	}
	return out
}

// Store edits a workflow's memory files. Every operation reads the files
// again, changes them and writes the ones that changed (atomicfs), all under
// one process-wide lock, so the dashboard's edits and the daemon's usage
// updates never lose each other's writes.
type Store struct {
	// WorkflowPath locates the project file.
	WorkflowPath string
	// Now stamps created/updated; nil means time.Now.
	Now func() time.Time
}

// writeMu serializes every read-modify-write of the memory files in this
// process.
var writeMu sync.Mutex

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// files is the working set of one operation: both files, read fresh.
type files struct {
	byScope map[Scope]*File
	dirty   map[Scope]bool
}

func (s Store) load() files {
	set := files{byScope: map[Scope]*File{}, dirty: map[Scope]bool{}}
	for _, f := range ReadAll(s.WorkflowPath) {
		set.byScope[f.Scope] = &f
	}
	return set
}

// find returns the scope and index of the entry with id.
func (f files) find(id string) (Scope, int, bool) {
	for _, sc := range Scopes {
		file := f.byScope[sc]
		if file == nil || file.Err != nil {
			continue
		}
		if i := slices.IndexFunc(file.Entries, func(e Entry) bool { return strings.EqualFold(e.ID, id) }); i >= 0 {
			return sc, i, true
		}
	}
	return "", 0, false
}

// writable returns the scope's file, refusing a scope without a file or a
// file that could not be read (rewriting it would lose what is there).
func (f files) writable(sc Scope) (*File, error) {
	file := f.byScope[sc]
	if file == nil || file.Path == "" {
		return nil, fmt.Errorf("%w: %s", ErrScopeOff, sc)
	}
	if file.Err != nil {
		return nil, fmt.Errorf("%w: fix the %s memory file first: %w", ErrInvalid, sc, file.Err)
	}
	return file, nil
}

// save writes every changed file. The project file goes first, so a move
// interrupted between the two writes leaves the entry in both files rather
// than in neither.
func (f files) save() error {
	for _, sc := range []Scope{ScopeProject, ScopePrivate} {
		if !f.dirty[sc] {
			continue
		}
		file := f.byScope[sc]
		if len(file.Entries) > MaxEntries {
			return fmt.Errorf("%w: the %s memory file would hold %d entries, at most %d", ErrTooLarge, sc, len(file.Entries), MaxEntries)
		}
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			return fmt.Errorf("memory: %w", err)
		}
		if err := atomicfs.WriteFile(file.Path, encode(file.Entries), filePerm(sc)); err != nil {
			return fmt.Errorf("memory: write %s: %w", file.Path, err)
		}
	}
	return nil
}

// List reads both files.
func (s Store) List() []File { return ReadAll(s.WorkflowPath) }

// NewID returns a fresh entry id.
func NewID() string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return "m-" + hex.EncodeToString(b[:])
}

// Create adds an entry to the file of e.Scope (private when empty). An
// empty id gets a fresh one; an empty type is a fact, an empty status
// active. Created/Updated default to now; a restore (undo of a delete)
// passes the deleted entry back with its id, timestamps, source and usage.
// Secrets are redacted before validation.
func (s Store) Create(e Entry) (Entry, error) {
	if err := checkRawSize(e.Text); err != nil {
		return Entry{}, err
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	now := s.now()
	e = scrub(e)
	if e.Scope == "" {
		e.Scope = ScopePrivate
	}
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.Type == "" {
		e.Type = TypeFact
	}
	if e.Status == "" {
		e.Status = StatusActive
	}
	if e.Created.IsZero() {
		e.Created = now
	}
	if e.Updated.IsZero() {
		e.Updated = now
	}
	if err := validate(e); err != nil {
		return Entry{}, err
	}
	fs := s.load()
	file, err := fs.writable(e.Scope)
	if err != nil {
		return Entry{}, err
	}
	if _, _, dup := fs.find(e.ID); dup {
		return Entry{}, fmt.Errorf("%w: the id %q is already used", ErrConflict, e.ID)
	}
	file.Entries = append(file.Entries, e)
	fs.dirty[e.Scope] = true
	return e, fs.save()
}

// Patch is a partial update of an entry's editable fields; nil leaves a
// field as it is. Scope changes go through Promote.
type Patch struct {
	Type     *Type     `json:"type,omitempty"`
	Text     *string   `json:"text,omitempty"`
	Profiles *[]string `json:"profiles,omitempty"`
	Tags     *[]string `json:"tags,omitempty"`
	Pinned   *bool     `json:"pinned,omitempty"`
	Status   *Status   `json:"status,omitempty"`
}

// Update applies p to the entry with id, redacting secrets, and stamps
// Updated.
func (s Store) Update(id string, p Patch) (Entry, error) {
	if p.Text != nil {
		if err := checkRawSize(*p.Text); err != nil {
			return Entry{}, err
		}
	}
	return s.modify(id, func(e *Entry) {
		if p.Type != nil {
			e.Type = *p.Type
		}
		if p.Text != nil {
			e.Text = *p.Text
		}
		if p.Profiles != nil {
			e.Profiles = *p.Profiles
		}
		if p.Tags != nil {
			e.Tags = *p.Tags
		}
		if p.Pinned != nil {
			e.Pinned = *p.Pinned
		}
		if p.Status != nil {
			e.Status = *p.Status
		}
	})
}

// Pin pins or unpins the entry with id.
func (s Store) Pin(id string, pinned bool) (Entry, error) {
	return s.Update(id, Patch{Pinned: &pinned})
}

func (s Store) modify(id string, change func(*Entry)) (Entry, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	fs := s.load()
	sc, i, ok := fs.find(id)
	if !ok {
		return Entry{}, ErrNotFound
	}
	file, err := fs.writable(sc)
	if err != nil {
		return Entry{}, err
	}
	e := file.Entries[i]
	change(&e)
	e = scrub(e)
	e.Updated = s.now()
	if err := validate(e); err != nil {
		return Entry{}, err
	}
	file.Entries[i] = e
	fs.dirty[sc] = true
	return e, fs.save()
}

// Delete removes the entry with id and returns it (the dashboard's undo
// creates it again).
func (s Store) Delete(id string) (Entry, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	fs := s.load()
	sc, i, ok := fs.find(id)
	if !ok {
		return Entry{}, ErrNotFound
	}
	file, err := fs.writable(sc)
	if err != nil {
		return Entry{}, err
	}
	e := file.Entries[i]
	file.Entries = slices.Delete(slices.Clone(file.Entries), i, i+1)
	fs.dirty[sc] = true
	return e, fs.save()
}

// Promote moves a private entry into the project's shared file, keeping its
// id, provenance and usage. ErrConflict when it is already shared.
func (s Store) Promote(id string) (Entry, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	fs := s.load()
	sc, i, ok := fs.find(id)
	if !ok {
		return Entry{}, ErrNotFound
	}
	if sc == ScopeProject {
		return Entry{}, fmt.Errorf("%w: %q is already in the project memory", ErrConflict, id)
	}
	from, err := fs.writable(sc)
	if err != nil {
		return Entry{}, err
	}
	to, err := fs.writable(ScopeProject)
	if err != nil {
		return Entry{}, err
	}
	e := scrub(from.Entries[i])
	e.Scope = ScopeProject
	e.Updated = s.now()
	if err := validate(e); err != nil {
		return Entry{}, err
	}
	to.Entries = append(to.Entries, e)
	from.Entries = slices.Delete(slices.Clone(from.Entries), i, i+1)
	fs.dirty[ScopeProject], fs.dirty[sc] = true, true
	return e, fs.save()
}

// RecordUse bumps lastUsed and useCount of the entries with ids. Ids no
// longer in either file (deleted since the run started) are skipped, and a
// file that cannot be read is left alone. The orchestrator calls it off its
// event loop, from the handler of the run's memory-used event.
func (s Store) RecordUse(ids []string, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	at = at.UTC()
	fs := s.load()
	for _, id := range ids {
		sc, i, ok := fs.find(id)
		if !ok {
			continue
		}
		file := fs.byScope[sc]
		if file.Path == "" {
			continue
		}
		e := &file.Entries[i]
		e.UseCount++
		t := at
		e.LastUsed = &t
		fs.dirty[sc] = true
	}
	return fs.save()
}
