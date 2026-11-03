package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// notifySettingsStore holds the hub's notification settings in memory,
// backed by StateDir/notifications.json. The hub is the only writer (PUT
// /hub/api/v1/notifications); the file is read when the hub starts.
type notifySettingsStore struct {
	path string
	mu   sync.RWMutex
	cur  NotificationSettings
}

// loadNotifySettings reads path. A missing file is the defaults; a file
// that does not parse is reported and left untouched (the defaults apply
// until the next save replaces it).
func loadNotifySettings(path string) *notifySettingsStore {
	s := &notifySettingsStore{path: path, cur: DefaultNotificationSettings()}
	data, err := readCapped(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		slog.Warn("hub: cannot read the notification settings; using the defaults", "path", path, "error", err)
	default:
		parsed, perr := ParseNotificationSettings(data)
		if perr != nil {
			slog.Warn("hub: the notification settings do not parse; using the defaults", "path", path, "error", perr)
			break
		}
		s.cur = parsed
	}
	return s
}

func readCapped(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxNotificationsFileBytes {
		return nil, errors.New("hub: the notification settings file is too large")
	}
	return os.ReadFile(path)
}

// get returns a copy of the current settings.
func (s *notifySettingsStore) get() NotificationSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur.clone()
}

// route is NotificationSettings.Route over the current settings.
func (s *notifySettingsStore) route(project, kind string, now time.Time) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur.Route(project, kind, now)
}

// snoozed is NotificationSettings.Snoozed over the current settings.
func (s *notifySettingsStore) snoozed(project string, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur.Snoozed(project, now)
}

// digestInterval is the current digest interval.
func (s *notifySettingsStore) digestInterval() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return time.Duration(s.cur.DigestIntervalMinutes) * time.Minute
}

// put normalizes ns, writes it 0600 and makes it current.
func (s *notifySettingsStore) put(ns NotificationSettings) (NotificationSettings, error) {
	if err := ns.validate(); err != nil {
		return NotificationSettings{}, err
	}
	ns.normalize()
	data, err := json.MarshalIndent(ns, "", "  ")
	if err != nil {
		return NotificationSettings{}, fmt.Errorf("hub: notification settings: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := atomicfs.WriteFile(s.path, append(data, '\n'), 0o600); err != nil {
		return NotificationSettings{}, fmt.Errorf("hub: save notification settings: %w", err)
	}
	s.cur = ns
	return ns.clone(), nil
}
