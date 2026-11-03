// Package hub is the itervox hub (#228): the first daemon started on a
// machine serves one URL for every project on it.
//
// Election. Every daemon that takes part (hub.enabled, no --no-hub) runs
// Run. The one that takes the flock'd hub.lock in the daemon registry
// directory is the hub; the others watch the lock (fsnotify plus a short
// poll) and the next live one takes over within a few seconds of the hub
// stopping, SIGKILL included.
//
// The hub serves, on its own listener (hub.bind:hub.port, loopback by
// default):
//
//   - GET /hub/api/v1/projects: the members (live registry records that
//     opted in) with their summary, health, version and capabilities;
//   - /hub/p/{project}/api/v1/...: a reverse proxy to that member's API. The
//     hub token's scope is checked first, against the member's own route
//     scope table (GET /api/v1/route-scopes); the incoming Authorization is
//     dropped and the hub's stored member token injected. SSE passes through;
//   - GET /hub/api/v1/feed?since=: every member's event feed merged in one
//     sequence, each event carrying its project, with per-member cursors
//     persisted in hub_feed_cursors.json; GET /hub/api/v1/events announces
//     new events over SSE;
//   - /hub/ and /hub/p/{project}/: the dashboard (a project switcher, then
//     the unchanged per-project dashboard over the proxied API).
//
// Merged events go to every enabled Notifier, at least once, with a
// per-notifier cursor and backoff.
//
// Phones (#231). The hub serves the dashboard as an installable web app
// (manifest, icons and service worker come with the embedded bundle) with
// a mobile layout at /hub/m. A built-in "push" channel sends Web Push
// (RFC 8291 encryption, a local VAPID key) to the devices subscribed with
// POST /hub/api/v1/push/subscribe; a phone pairs by exchanging a one-time
// QR code for its own operator token (pairing.go); server.allowed_origins
// lets a dashboard hosted on another origin call the hub (internal/cors).
//
// Notifications (#229). StateDir/notifications.json routes each
// notification kind per project to the channels push, slack and desktop,
// with a per-project snooze and a digest interval (NotificationSettings,
// GET/PUT /hub/api/v1/notifications). Every registered Channel (desktop
// built in, Config.Channels for the rest) gets a channel notifier that
// sends urgent kinds at once and batches the others into digests; the
// desktop channel publishes on GET /hub/api/v1/events for the Mac app.
// Snooze and digests are hub state, never a member's.
//
// Event-loop rule: the hub runs in a daemon process but outside its
// orchestrator event loop. It never touches the hosting project's state
// except through that project's HTTP API, exactly like any other member.
//
// The package imports the standard library, chi, fsnotify and
// internal/apitokens, internal/atomicfs, internal/cors, internal/logging
// and internal/metrics only.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/vnovick/itervox/internal/metrics"
)

// LockFileName is the hub lock inside the daemon registry directory. Its
// content, while a hub holds it, is a LockInfo; supervisors (the Mac app)
// read it to find the hub.
const LockFileName = "hub.lock"

// Defaults of the hub configuration.
const (
	DefaultPort = 8089
	DefaultBind = "127.0.0.1"
)

// lockInfoSchemaVersion versions LockInfo. Fields may be added within a
// version; a rename, removal or change of meaning bumps it.
const lockInfoSchemaVersion = 1

// LockInfo is the content of hub.lock while a hub holds it. It never holds
// a token.
type LockInfo struct {
	SchemaVersion int    `json:"schema_version"`
	PID           int    `json:"pid"`
	URL           string `json:"url"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Version       string `json:"version"`
	Workflow      string `json:"workflow"`
	StartedAt     string `json:"started_at"`
}

// Config wires a hub. cmd/itervox fills it from the hosting daemon's
// load-time settings (hub.*) and its registry and token helpers.
type Config struct {
	// RegistryDir is the daemon registry directory: it holds hub.lock and
	// the member records the hub watches.
	RegistryDir string
	// StateDir holds the hub's own state: tokens.json (the hub token store)
	// and hub_feed_cursors.json. Created 0700.
	StateDir string
	// Bind and Port are where the hub listens; when Port is busy the next
	// free one (up to MaxPortTries) is used and recorded in hub.lock.
	Bind string
	Port int
	// Version is reported in hub.lock and /hub/api/v1/projects.
	Version string
	// Workflow is the hosting daemon's WORKFLOW.md, reported in hub.lock.
	Workflow string
	// Members lists the live registry records that opted in to the hub.
	Members func() []MemberRecord
	// MemberToken returns the hub's stored token of scope for a member.
	MemberToken TokenFunc
	// SPA serves the embedded dashboard (index.html fallback included).
	SPA http.Handler
	// Notifiers receive the merged feed.
	Notifiers []Notifier
	// Channels are the notification channels set up besides the built-in
	// desktop and push ones (#229; Slack #230). Each is routed by the
	// notification settings in StateDir/notifications.json. A channel name
	// outside ChannelNames, or a repeat, is skipped.
	Channels []Channel
	// AllowedOrigins is the hosting daemon's server.allowed_origins (#231):
	// the origins (normalized, internal/cors) whose pages may call the hub
	// cross-origin. Empty: same-origin only.
	AllowedOrigins []string
	// PushSubject is the VAPID contact (DefaultVAPIDSubject when empty).
	PushSubject string
	// PushHTTPClient sends Web Push requests (a 15 s timeout client when
	// nil); redirects are never followed. Tests point it at a fake.
	PushHTTPClient *http.Client
	// PushEndpointAllowed decides which subscription endpoints the hub
	// accepts and sends to (https on DefaultPushServices when nil).
	PushEndpointAllowed func(*url.URL) bool

	// Tunables; zero means the default in parentheses.
	ElectionPoll    time.Duration // how often a follower retries the lock (1s)
	Rescan          time.Duration // member safety rescan (10s)
	FeedPoll        time.Duration // member feed poll (2s)
	NotifierBackoff time.Duration // first notifier retry delay, doubling to a minute (1s)
	MaxPortTries    int           // ports tried from Port upward (10)
}

func (c *Config) withDefaults() {
	if c.Bind == "" {
		c.Bind = DefaultBind
	}
	if c.ElectionPoll <= 0 {
		c.ElectionPoll = time.Second
	}
	if c.Rescan <= 0 {
		c.Rescan = 10 * time.Second
	}
	if c.FeedPoll <= 0 {
		c.FeedPoll = 2 * time.Second
	}
	if c.NotifierBackoff <= 0 {
		c.NotifierBackoff = time.Second
	}
	if c.MaxPortTries <= 0 {
		c.MaxPortTries = 10
	}
	if c.Members == nil {
		c.Members = func() []MemberRecord { return nil }
	}
	if c.SPA == nil {
		c.SPA = http.NotFoundHandler()
	}
}

// LockPath is hub.lock in registryDir.
func LockPath(registryDir string) string { return filepath.Join(registryDir, LockFileName) }

// ReadLockInfo returns the running hub's LockInfo. ok is false when no hub
// holds the lock (or it has not published its address yet).
func ReadLockInfo(registryDir string) (info LockInfo, ok bool, err error) {
	path := LockPath(registryDir)
	held, err := lockHeld(path)
	if err != nil || !held {
		return LockInfo{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return LockInfo{}, false, fmt.Errorf("hub: read lock: %w", err)
	}
	if len(data) == 0 || json.Unmarshal(data, &info) != nil || info.Port == 0 {
		return LockInfo{}, false, nil
	}
	return info, true, nil
}

// Run takes part in the hub election until ctx ends: it serves as the hub
// whenever it holds the lock, and otherwise waits for the lock to come free.
// It returns nil when ctx ends.
func Run(ctx context.Context, cfg Config) error {
	cfg.withDefaults()
	if !lockSupported {
		slog.Info("hub: not available on this platform (no flock)")
		return nil
	}
	if err := os.MkdirAll(cfg.RegistryDir, 0o700); err != nil {
		return fmt.Errorf("hub: registry dir: %w", err)
	}
	lockPath := LockPath(cfg.RegistryDir)
	wake := make(chan struct{}, 1)
	if w, err := fsnotify.NewWatcher(); err != nil {
		slog.Debug("hub: no fsnotify watcher; polling the lock only", "error", err)
	} else {
		defer func() { _ = w.Close() }()
		if err := w.Add(cfg.RegistryDir); err != nil {
			slog.Debug("hub: cannot watch the registry; polling the lock only", "error", err)
		}
		go func() {
			defer recoverHubGoroutine("hub-election-watch")
			forwardEvents(ctx, w, func(name string) bool { return filepath.Base(name) == LockFileName }, wake)
		}()
	}
	retryAfterFailure := time.Time{}
	for {
		if time.Now().After(retryAfterFailure) {
			lock, acquired, err := tryLock(lockPath)
			switch {
			case err != nil:
				slog.Warn("hub: cannot take part in the hub election", "error", err)
				retryAfterFailure = time.Now().Add(30 * time.Second)
			case acquired:
				serveErr := serveAsHub(ctx, cfg, lock)
				lock.Release()
				if ctx.Err() != nil {
					return nil
				}
				if serveErr != nil {
					// Let another daemon try; do not flap on a lasting failure.
					slog.Warn("hub: stopped serving as the hub", "error", serveErr)
					retryAfterFailure = time.Now().Add(30 * time.Second)
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-time.After(cfg.ElectionPoll):
		}
	}
}

// forwardEvents turns fsnotify events on matching names into a non-blocking
// wake signal until ctx ends or the watcher closes.
func forwardEvents(ctx context.Context, w *fsnotify.Watcher, match func(string) bool, wake chan<- struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if match(ev.Name) {
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		case _, ok := <-w.Errors:
			if !ok {
				return
			}
		}
	}
}

// serveAsHub runs the hub while this process holds lock, until ctx ends or
// the listener fails.
func serveAsHub(ctx context.Context, cfg Config, lock *Lock) error {
	h, err := newHub(cfg)
	if err != nil {
		return err
	}
	ln, addr, err := listenFrom(cfg.Bind, cfg.Port, cfg.MaxPortTries)
	if err != nil {
		return err
	}
	hubCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := &http.Server{
		Handler:           h.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: SSE streams (the hub's own and proxied ones) are
		// long-lived. Every handler ends with hubCtx.
		BaseContext: func(net.Listener) context.Context { return hubCtx },
	}
	info := LockInfo{
		SchemaVersion: lockInfoSchemaVersion,
		PID:           os.Getpid(),
		URL:           "http://" + addr.String() + "/hub/",
		Host:          addr.IP.String(),
		Port:          addr.Port,
		Version:       cfg.Version,
		Workflow:      cfg.Workflow,
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	h.self = info
	data, _ := json.MarshalIndent(info, "", "  ")
	if err := lock.publish(append(data, '\n')); err != nil {
		_ = ln.Close()
		return err
	}
	slog.Info("hub: this daemon is the itervox hub", "url", info.URL)
	h.start(hubCtx)
	serveDone := make(chan error, 1)
	go func() {
		defer recoverHubGoroutine("hub-http")
		serveDone <- srv.Serve(ln)
	}()
	select {
	case <-ctx.Done():
	case err = <-serveDone:
	}
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutCancel()
	if srv.Shutdown(shutCtx) != nil {
		_ = srv.Close()
	}
	h.stop()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("hub: serve: %w", err)
	}
	return nil
}

// recoverHubGoroutine is the hub's panic containment (CORE-008), deferred
// directly at the top of every hub goroutine: a panic is logged with its
// stack and counted, and ends only that goroutine. The hub must never take
// its hosting daemon (and the agents it runs) down.
func recoverHubGoroutine(name string) {
	r := recover()
	if r == nil {
		return
	}
	metrics.GoroutinePanic()
	slog.Error("hub: goroutine panic recovered", "goroutine", name, "panic", r, "stack", string(debug.Stack()))
}
