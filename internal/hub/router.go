package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/apitokens"
)

// Hub is a serving hub: its member set, merged feed and HTTP surface.
type Hub struct {
	cfg       Config
	tokens    *apitokens.Store
	client    *memberClient
	members   *memberSet
	feed      *feed
	cursors   *cursors
	notifiers []Notifier
	// notify is the notification settings (#229); channels the registered
	// channels by name (desktop always); chanNotifiers their notifiers.
	notify        *notifySettingsStore
	desktop       *desktopChannel
	channels      map[string]Channel
	chanNotifiers []*channelNotifier
	// self is what this hub published in hub.lock (zero in unit tests).
	self   LockInfo
	router chi.Router
	wg     sync.WaitGroup
}

// newHub builds a hub over cfg's state directory (created 0700; a symlink
// is refused).
func newHub(cfg Config) (*Hub, error) {
	cfg.withDefaults()
	if err := ensurePrivateDir(cfg.StateDir); err != nil {
		return nil, err
	}
	client := newMemberClient(cfg.MemberToken)
	h := &Hub{
		cfg:     cfg,
		tokens:  apitokens.Open(filepath.Join(cfg.StateDir, TokenStoreFileName)),
		client:  client,
		members: newMemberSet(client),
		cursors: loadCursors(filepath.Join(cfg.StateDir, CursorFileName)),
	}
	h.feed = newFeed(h.cursors.headSeq())
	h.setupChannels(cfg)
	notifiers := slices.Clone(cfg.Notifiers)
	for _, cn := range h.chanNotifiers {
		notifiers = append(notifiers, cn)
	}
	seen := map[string]bool{}
	for _, n := range notifiers {
		if n == nil || n.Name() == "" || seen[n.Name()] {
			slog.Warn("hub: notifier skipped (missing or duplicate name)")
			continue
		}
		seen[n.Name()] = true
		h.notifiers = append(h.notifiers, n)
		h.cursors.adoptNotifier(n.Name())
	}
	h.router = h.routes()
	return h, nil
}

// ensurePrivateDir creates dir 0700 and refuses a symlink in its place.
func ensurePrivateDir(dir string) error {
	if dir == "" {
		return errors.New("hub: no state directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("hub: state dir: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("hub: state dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("hub: state dir %s is not a directory (symlink refused)", dir)
	}
	if info.Mode().Perm() != 0o700 {
		_ = os.Chmod(dir, 0o700)
	}
	return nil
}

// Handler is the hub's HTTP surface.
func (h *Hub) Handler() http.Handler { return h.router }

func (h *Hub) routes() chi.Router {
	r := chi.NewRouter()
	r.Use(securityHeaders)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/hub/", http.StatusFound)
	})
	r.Get("/hub/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "hub": true})
	})
	r.Group(func(r chi.Router) {
		r.Use(h.authMiddleware)
		r.Use(scopeMiddleware)
		r.Get("/hub/api/v1/whoami", h.handleWhoami)
		r.Get("/hub/api/v1/projects", h.handleProjects)
		r.Get("/hub/api/v1/feed", h.handleFeed)
		r.Get("/hub/api/v1/events", h.handleEvents)
		r.Get("/hub/api/v1/notifications", h.handleGetNotifications)
		r.Put("/hub/api/v1/notifications", h.handlePutNotifications)
		r.Post("/hub/api/v1/notifications/test", h.handleTestNotification)
		r.Handle("/hub/p/{project}/api/*", http.HandlerFunc(h.handleProxy))
	})
	r.Handle("/hub/api/*", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such hub route")
	}))
	r.Handle("/*", h.cfg.SPA)
	return r
}

// securityHeaders sets the daemons' default-deny hardening headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "frame-ancestors 'none'")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// start runs the hub's background work until ctx ends.
func (h *Hub) start(ctx context.Context) {
	run := func(name string, fn func(context.Context)) {
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			defer recoverHubGoroutine(name)
			fn(ctx)
		}()
	}
	run("hub-discovery", h.discover)
	run("hub-feed", h.pollFeeds)
	run("hub-cursor-saver", h.saveCursors)
	// #230: interactive channels (Slack's Socket Mode) hold their own
	// connection only while this daemon serves the hub.
	for _, name := range ChannelNames {
		if ic, ok := h.channels[name].(InteractiveChannel); ok {
			run("hub-channel-"+name, func(ctx context.Context) { ic.Run(ctx, hubActions{h: h}) })
		}
	}
	for _, n := range h.notifiers {
		run("hub-notifier-"+n.Name(), func(ctx context.Context) { h.runNotifier(ctx, n) })
	}
	for _, cn := range h.chanNotifiers {
		run("hub-digest-"+cn.ch.Name(), cn.runDigest)
	}
}

// stop waits for the background work (after its ctx ended) and saves the
// cursors one last time.
func (h *Hub) stop() {
	h.wg.Wait()
	if err := h.cursors.save(); err != nil {
		slog.Warn("hub: could not save the feed cursors", "error", err)
	}
}

// saveCursors writes changed cursors twice a second until ctx ends.
func (h *Hub) saveCursors(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := h.cursors.save(); err != nil {
				slog.Warn("hub: could not save the feed cursors", "error", err)
			}
		}
	}
}

// listenFrom binds bind:port, or the next free port up to tries ports.
func listenFrom(bind string, port, tries int) (net.Listener, *net.TCPAddr, error) {
	var lastErr error
	for i := range max(tries, 1) {
		p := port + i
		if port == 0 {
			p = 0
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(p)))
		if err == nil {
			addr, _ := ln.Addr().(*net.TCPAddr)
			if i > 0 {
				slog.Info("hub: hub.port is busy; using the next free port", "configured", port, "port", addr.Port)
			}
			return ln, addr, nil
		}
		lastErr = err
		if port == 0 || !errors.Is(err, syscall.EADDRINUSE) {
			break
		}
	}
	return nil, nil, fmt.Errorf("hub: cannot listen on %s from port %d: %w", bind, port, lastErr)
}
