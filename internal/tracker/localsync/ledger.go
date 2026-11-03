package localsync

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// The sync ledger (.itervox/sync_ledger.json) is runtime state, gitignored
// with the other .itervox/*.json files: what the two sides last agreed on,
// per issue and per field, so a cycle can tell which side changed a field
// since. Losing it is safe: links are re-made from the files' `remote:`
// keys (adopt), and the first cycle after that keeps the newer side of any
// field that differs.

const ledgerVersion = 1

type ledger struct {
	Version int `json:"version"`
	// Cursor is the newest remote update time seen; incremental pulls ask
	// for changes since it.
	Cursor time.Time `json:"cursor,omitzero"`
	// LastFullAt is when the last full pull ran.
	LastFullAt time.Time `json:"last_full_at,omitzero"`
	// Links is keyed by local identifier.
	Links map[string]*link `json:"links"`
}

// link is one local issue file and its remote issue.
type link struct {
	Local            string `json:"local"`
	RemoteID         string `json:"remote_id,omitempty"`
	RemoteIdentifier string `json:"remote_identifier,omitempty"`
	// CreateKey is set while the remote issue is being created (an
	// outbox create_issue entry is queued): the issue is not synced until
	// the create lands and RemoteID is known.
	CreateKey string `json:"create_key,omitempty"`
	// Detached is set when the local file was deleted: the remote issue is
	// left alone and not pulled into a new file again.
	Detached bool `json:"detached,omitempty"`
	// RemoteUpdatedAt is the remote issue's update time when last read.
	RemoteUpdatedAt time.Time `json:"remote_updated_at,omitzero"`
	// Fields holds the last-synced value of each field.
	Fields map[string]fieldBase `json:"fields,omitempty"`
	// LocalComments are the hashes of the file's comments already handled
	// (pushed, pulled from the remote, or local-only such as a conflict
	// note); RemoteComments the remote comment IDs already handled.
	LocalComments  map[string]bool `json:"local_comments,omitempty"`
	RemoteComments map[string]bool `json:"remote_comments,omitempty"`
	// PushedKeys are the idempotency keys of the comments the sync posted,
	// so their echo is recognised (Linear: the comment's id; GitHub: a
	// hidden marker in its body).
	PushedKeys map[string]bool `json:"pushed_keys,omitempty"`
}

// fieldBase is a field's last-synced value (as a hash) and when it was
// agreed.
type fieldBase struct {
	Hash     string    `json:"hash"`
	SyncedAt time.Time `json:"synced_at"`
}

func newLink(localID string) *link {
	return &link{Local: localID, Fields: map[string]fieldBase{}, LocalComments: map[string]bool{},
		RemoteComments: map[string]bool{}, PushedKeys: map[string]bool{}}
}

// ensureMaps fills maps a loaded ledger left nil.
func (l *link) ensureMaps() {
	if l.Fields == nil {
		l.Fields = map[string]fieldBase{}
	}
	if l.LocalComments == nil {
		l.LocalComments = map[string]bool{}
	}
	if l.RemoteComments == nil {
		l.RemoteComments = map[string]bool{}
	}
	if l.PushedKeys == nil {
		l.PushedKeys = map[string]bool{}
	}
}

func (l *link) setBase(field, hash string, at time.Time) {
	l.Fields[field] = fieldBase{Hash: hash, SyncedAt: at}
}

// loadLedger reads path; a missing or unreadable file starts an empty
// ledger (logged), never failing the daemon.
func loadLedger(path string) *ledger {
	lg := &ledger{Version: ledgerVersion, Links: map[string]*link{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("local sync: cannot read the sync ledger; starting empty", "path", path, "error", err)
		}
		return lg
	}
	if err := json.Unmarshal(data, lg); err != nil || lg.Version != ledgerVersion {
		slog.Warn("local sync: the sync ledger does not parse; starting empty", "path", path, "error", err)
		return &ledger{Version: ledgerVersion, Links: map[string]*link{}}
	}
	if lg.Links == nil {
		lg.Links = map[string]*link{}
	}
	for id, l := range lg.Links {
		if l == nil {
			delete(lg.Links, id)
			continue
		}
		l.ensureMaps()
	}
	return lg
}

func (lg *ledger) save(path string) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(lg, "", "  ")
	if err != nil {
		return fmt.Errorf("local sync: marshal ledger: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("local sync: %w", err)
	}
	if err := atomicfs.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("local sync: write ledger: %w", err)
	}
	return nil
}

// byRemoteID returns the link for a remote issue ID.
func (lg *ledger) byRemoteID(id string) *link {
	for _, l := range lg.Links {
		if l.RemoteID == id {
			return l
		}
	}
	return nil
}
