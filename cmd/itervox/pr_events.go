package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/prwatch"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// prEventOrchestrator is what the PR-event watcher (#173) needs from the
// orchestrator.
type prEventOrchestrator interface {
	ManagedPRs() []orchestrator.ManagedPR
	PREventTriggerTypes() map[string]bool
	DispatchPREventAutomations(ctx context.Context, issue domain.Issue, ev orchestrator.PREvent) int
}

// ghAuthToken reads the gh CLI's stored token for host. A seam for tests.
var ghAuthToken = func(ctx context.Context, host string) string {
	out, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", host).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// prEventsTokenSource resolves the GitHub token the PR-event watcher sends
// for a host, once per host: tracker.api_key when the tracker is GitHub on
// github.com; else GH_TOKEN or GITHUB_TOKEN (GH_ENTERPRISE_TOKEN or
// GITHUB_ENTERPRISE_TOKEN for another host); else the gh CLI's login
// (`gh auth token`), the same credential the worker's gh calls use. The
// token is registered with log redaction and never logged.
func prEventsTokenSource(cfg *config.Config) func(host string) string {
	var mu sync.Mutex
	resolved := map[string]string{}
	return func(host string) string {
		mu.Lock()
		defer mu.Unlock()
		if tok, ok := resolved[host]; ok {
			return tok
		}
		tok := ""
		if cfg.Tracker.Kind == "github" && host == "github.com" && cfg.Tracker.APIKey != "" &&
			(cfg.Tracker.Endpoint == "" || strings.Contains(cfg.Tracker.Endpoint, "api.github.com")) {
			tok = cfg.Tracker.APIKey
		}
		envKeys := []string{"GH_TOKEN", "GITHUB_TOKEN"}
		if host != "github.com" {
			envKeys = []string{"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
		}
		for _, k := range envKeys {
			if tok == "" {
				tok = strings.TrimSpace(os.Getenv(k))
			}
		}
		if tok == "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			tok = ghAuthToken(ctx, host)
			cancel()
		}
		if tok == "" {
			slog.Warn("pr events: no GitHub token for this host; PR polling is unauthenticated (60 requests/hour) and private repositories are unreadable. Set GITHUB_TOKEN or run `gh auth login`.",
				"host", host)
		} else {
			logging.RegisterSecret(tok)
		}
		resolved[host] = tok
		return tok
	}
}

// prEventSink turns a watcher event into PR-event automation dispatches: it
// resolves the managed PR's issue and hands both to the orchestrator, whose
// event loop decides. Events of a kind no rule uses are dropped before any
// tracker read.
func prEventSink(tr tracker.Tracker, orch prEventOrchestrator) func(context.Context, prwatch.Event) {
	return func(ctx context.Context, ev prwatch.Event) {
		if !orch.PREventTriggerTypes()[ev.Kind] || ev.Identifier == "" {
			return
		}
		fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		issue, err := tr.FetchIssueByIdentifier(fctx, ev.Identifier)
		cancel()
		if err != nil || issue == nil {
			if ctx.Err() == nil {
				slog.Warn("pr events: issue for a managed PR not readable; event skipped",
					"identifier", ev.Identifier, "pr_url", ev.PRURL, "kind", ev.Kind, "error", err)
			}
			return
		}
		orch.DispatchPREventAutomations(ctx, *issue, orchestrator.PREvent{
			TriggerType:       ev.Kind,
			PRURL:             ev.PRURL,
			PRNumber:          ev.PRNumber,
			Branch:            ev.Branch,
			HeadSHA:           ev.HeadSHA,
			CheckName:         ev.CheckName,
			CheckConclusion:   ev.Conclusion,
			CheckCompletedAt:  ev.CompletedAt,
			MergeableState:    ev.MergeableState,
			ReviewID:          ev.ReviewID,
			ReviewAuthor:      ev.ReviewAuthor,
			ReviewState:       ev.ReviewState,
			ReviewSubmittedAt: ev.SubmittedAt,
			Source:            ev.Source,
		})
	}
}

// managedPRsFor lists the orchestrator's managed PRs for the watcher.
func managedPRsFor(orch prEventOrchestrator) func() []prwatch.Managed {
	return func() []prwatch.Managed {
		prs := orch.ManagedPRs()
		out := make([]prwatch.Managed, 0, len(prs))
		for _, pr := range prs {
			out = append(out, prwatch.Managed{URL: pr.PRURL, Identifier: pr.Identifier, Since: pr.Since})
		}
		return out
	}
}

// newPREventWatcher builds the PR-event watcher (#173) over client.
func newPREventWatcher(cfg *config.Config, tr tracker.Tracker, orch prEventOrchestrator, client *prwatch.Client) *prwatch.Watcher {
	return prwatch.New(prwatch.Config{
		Client:   client,
		Managed:  managedPRsFor(orch),
		Kinds:    orch.PREventTriggerTypes,
		Emit:     prEventSink(tr, orch),
		Webhooks: cfg.PREvents.WebhookSecret != "",
	})
}

// startPREvents starts the PR-event watcher (#173): a poll of the open
// managed PRs on polling.automation_interval_ms (stretched like the
// automation event poll while GitHub's request budget is low; nothing is
// fetched while no PR-event rule is configured), and, with
// pr_events.webhook_secret set, the webhook receiver it returns for
// POST /api/v1/webhooks/pr-events. Runs until ctx ends.
func startPREvents(ctx context.Context, cfg *config.Config, tr tracker.Tracker, orch prEventOrchestrator) server.WebhookHandler {
	client := &prwatch.Client{Token: prEventsTokenSource(cfg)}
	w := newPREventWatcher(cfg, tr, orch, client)
	go func() {
		defer failFastOnPanic("pr-events-poll")
		cadence := newPollCadence(cfg.Polling.AutomationIntervalMs,
			"pr events: GitHub request budget is low, slowing the PR event poll",
			"pr events: GitHub request budget recovered, PR event poll back to its interval")
		timer := time.NewTimer(cadence.nextDelay(nil))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				w.Poll(ctx)
				timer.Reset(cadence.nextDelay(client))
			}
		}
	}()
	if cfg.PREvents.WebhookSecret == "" {
		return nil
	}
	logging.RegisterSecret(cfg.PREvents.WebhookSecret)
	h, err := prwatch.NewHandler(cfg.PREvents.WebhookSecret, w)
	if err != nil {
		slog.Warn("pr events: webhooks not used", "error", err)
		return nil
	}
	go func() {
		defer failFastOnPanic("pr-events-webhooks")
		h.Run(ctx)
	}()
	slog.Info("pr events: receiving GitHub webhooks", "path", "/api/v1/webhooks/"+h.Tracker())
	return h
}
