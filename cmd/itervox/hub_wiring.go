package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/hub"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// The itervox hub (#228) in the daemon process. The hub runs on its own
// goroutines and listener, OUTSIDE the orchestrator event loop: it never
// touches this project's orchestrator State, and reaches this project only
// through its HTTP API, exactly like every other member.

// noHubFlag is --no-hub: opt this daemon out of being or joining the hub.
var noHubFlag bool

// hubMembership is true when this daemon takes part in the hub. It is set
// once by maybeStartHub before the first run() generation writes the
// registry record, and published there as hub_member.
var hubMembership atomic.Bool

// The running hub participation; started once per process (hub.* settings
// are load-time: a WORKFLOW.md reload does not move the hub).
var (
	hubOnce   sync.Once
	hubStopMu sync.Mutex
	hubCancel context.CancelFunc
	hubDone   chan struct{}
)

// hubParticipates reports whether this daemon takes part in the hub:
// hub.enabled, no --no-hub, and not `itervox demo` (which writes nothing
// outside its scratch directory).
func hubParticipates(cfg *config.Config, noHub bool) bool {
	return cfg != nil && cfg.Hub.Enabled && !noHub && demoSession == nil
}

// hubStateDir is ~/.itervox/hub: the hub token store, feed cursors,
// notification settings, the hub's own .env (Slack tokens, #230), the
// Slack action signing key, and the Web Push VAPID key and device list
// (#231).
func hubStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("hub: %w", err)
	}
	return filepath.Join(home, ".itervox", "hub"), nil
}

// maybeStartHub starts this daemon's part in the hub election, once.
func maybeStartHub(ctx context.Context, cfg *config.Config, workflowPath string, noHub bool) {
	hubOnce.Do(func() {
		if !hubParticipates(cfg, noHub) {
			slog.Info("hub: this daemon does not take part in the hub (--no-hub or hub.enabled: false)")
			return
		}
		hcfg, err := hubConfigFor(cfg, workflowPath)
		if err != nil {
			slog.Warn("hub: not started", "error", err)
			return
		}
		hubMembership.Store(true)
		hctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		hubStopMu.Lock()
		hubCancel, hubDone = cancel, done
		hubStopMu.Unlock()
		go func() {
			defer close(done)
			defer orchestrator.RecoverGoroutine("hub", "", nil)
			if err := hub.Run(hctx, hcfg); err != nil {
				slog.Warn("hub: stopped", "error", err)
			}
		}()
	})
}

// stopHub ends this daemon's hub participation and waits briefly, so a
// serving hub empties hub.lock and the next daemon takes over at once.
func stopHub() {
	hubStopMu.Lock()
	cancel, done := hubCancel, hubDone
	hubStopMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// hubConfigFor builds the hub configuration from this daemon's settings.
func hubConfigFor(cfg *config.Config, workflowPath string) (hub.Config, error) {
	regDir, err := daemonRegistryDir()
	if err != nil {
		return hub.Config{}, err
	}
	stateDir, err := hubStateDir()
	if err != nil {
		return hub.Config{}, err
	}
	wf, _ := filepath.Abs(workflowPath)
	return hub.Config{
		RegistryDir: regDir,
		StateDir:    stateDir,
		Bind:        cfg.Hub.Bind,
		Port:        cfg.Hub.Port,
		Version:     version,
		Workflow:    wf,
		Members:     func() []hub.MemberRecord { return hubMembersIn(regDir) },
		MemberToken: hubMemberToken,
		SPA:         server.DashboardHandler(),
		Notifiers:   []hub.Notifier{hub.LogNotifier{}},
		Channels:    hubChannels(cfg, stateDir), // #230: Slack
		// #231: a dashboard hosted elsewhere may call the hub.
		AllowedOrigins: cfg.Server.AllowedOrigins,
	}, nil
}

// hubMembersIn lists the live registry records that joined the hub. A
// record without hub_member (a --no-hub daemon, or one older than the hub)
// is not a member.
func hubMembersIn(regDir string) []hub.MemberRecord {
	var out []hub.MemberRecord
	for _, r := range readDaemonRegistry(regDir) {
		if !r.HubMember || r.URL == "" {
			continue
		}
		out = append(out, hub.MemberRecord{
			PID: r.PID, Workflow: r.Workflow, ProjectDir: r.ProjectDir, URL: r.URL,
			Version: r.Version, StartedAt: r.StartedAt, TokenRequired: r.TokenRequired,
		})
	}
	return out
}

// hubTokenMu serializes member-token creation, so two concurrent first uses
// never create two tokens for one member.
var hubTokenMu sync.Mutex

// hubMemberToken is the hub's stored token of scope for a member: created
// in the member's own token store on first use and remembered in its
// .itervox/hub-<scope>-token.json (0600, gitignored), the same code path
// as `itervox mcp`'s token. Never printed; registered for log redaction.
func hubMemberToken(m hub.MemberRecord, scope apitokens.Scope) (string, error) {
	hubTokenMu.Lock()
	defer hubTokenMu.Unlock()
	secret, _, err := ensureStoredToken(m.Workflow, storedTokenSpec{
		file:  "hub-" + string(scope) + "-token.json",
		name:  "hub-" + string(scope),
		actor: "itervox hub",
		label: "hub",
	}, scope)
	if err != nil {
		return "", err
	}
	logging.RegisterSecret(secret)
	return secret, nil
}
