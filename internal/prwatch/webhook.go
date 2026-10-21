package prwatch

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/metrics"
)

// WebhookMetricsLabel is the tracker label of the webhook counters for PR
// event deliveries (itervox_tracker_webhooks_total{tracker="pr-events"}).
const WebhookMetricsLabel = "pr-events"

// maxWebhookBody bounds a delivery; PR, check and review events are a few
// tens of kilobytes.
const maxWebhookBody = 1 << 20

// webhookQueue bounds deliveries waiting to be applied; past it a delivery
// is dropped (the poll still sees the PR's head and mergeable state).
const webhookQueue = 256

// Handler serves POST /api/v1/webhooks/pr-events: signed GitHub check_run,
// status, pull_request and pull_request_review deliveries (#173). It answers
// 202 for an accepted or ignored delivery, 401 for a bad signature and 400
// for a payload it cannot read; Run applies what it accepted.
type Handler struct {
	secret  string
	watcher *Watcher
	queue   chan Event
	now     func() time.Time

	mu       sync.Mutex
	lastWarn time.Time
}

// NewHandler returns a Handler routing deliveries through w. It fails
// without a secret: unsigned deliveries are never accepted.
func NewHandler(secret string, w *Watcher) (*Handler, error) {
	if secret == "" {
		return nil, errors.New("prwatch: a webhook secret is required")
	}
	if w == nil {
		return nil, errors.New("prwatch: no watcher")
	}
	metrics.PreinitWebhooks(WebhookMetricsLabel)
	return &Handler{secret: secret, watcher: w, queue: make(chan Event, webhookQueue), now: time.Now}, nil
}

// Tracker is the webhook path segment the server routes to this handler.
func (h *Handler) Tracker() string { return WebhookMetricsLabel }

// Run applies queued deliveries until ctx ends.
func (h *Handler) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-h.queue:
			h.watcher.cfg.Emit(ctx, ev)
		}
	}
}

// ServeHTTP verifies, parses and queues one delivery.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil || len(body) > maxWebhookBody {
		h.reject(w, metrics.WebhookMalformed, http.StatusRequestEntityTooLarge, "payload too large or unreadable")
		return
	}
	if !validSignature(h.secret, body, strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")) {
		h.reject(w, metrics.WebhookBadSignature, http.StatusUnauthorized, "bad signature")
		return
	}
	events, err := h.watcher.parseDelivery(r.Header.Get("X-GitHub-Event"), body)
	if err != nil {
		h.reject(w, metrics.WebhookMalformed, http.StatusBadRequest, "unreadable payload")
		return
	}
	if len(events) == 0 {
		metrics.Webhook(WebhookMetricsLabel, metrics.WebhookIgnored)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	for _, ev := range events {
		select {
		case h.queue <- ev:
		default:
			slog.Warn("pr events: webhook queue full, delivery dropped", "pr_url", ev.PRURL, "kind", ev.Kind)
		}
	}
	metrics.Webhook(WebhookMetricsLabel, metrics.WebhookAccepted)
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) reject(w http.ResponseWriter, result string, status int, msg string) {
	metrics.Webhook(WebhookMetricsLabel, result)
	h.mu.Lock()
	warn := h.now().Sub(h.lastWarn) >= time.Minute
	if warn {
		h.lastWarn = h.now()
	}
	h.mu.Unlock()
	if warn {
		slog.Warn("pr events: webhook delivery dropped (further drops this minute are only counted)", "result", result)
	}
	http.Error(w, msg, status)
}

// validSignature compares the hex HMAC-SHA256 of body under secret with got
// in constant time.
func validSignature(secret string, body []byte, got string) bool {
	want, err := hex.DecodeString(strings.TrimSpace(got))
	if err != nil || len(want) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

type whRepo struct {
	HTMLURL string `json:"html_url"`
}

type whPR struct {
	HTMLURL        string `json:"html_url"`
	Number         int    `json:"number"`
	MergeableState string `json:"mergeable_state"`
	Head           struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
}

// parseDelivery turns one delivery into events for managed PRs. Other event
// types, actions, and unmanaged PRs yield nothing.
func (w *Watcher) parseDelivery(event string, body []byte) ([]Event, error) {
	switch event {
	case "check_run":
		var p struct {
			Action   string `json:"action"`
			CheckRun struct {
				Name         string    `json:"name"`
				HeadSHA      string    `json:"head_sha"`
				Status       string    `json:"status"`
				Conclusion   string    `json:"conclusion"`
				CompletedAt  time.Time `json:"completed_at"`
				PullRequests []whPR    `json:"pull_requests"`
			} `json:"check_run"`
			Repository whRepo `json:"repository"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		cr := p.CheckRun
		if p.Action != "completed" || cr.Conclusion == "" {
			return nil, nil
		}
		var out []Event
		for _, pr := range cr.PullRequests {
			if p.Repository.HTMLURL == "" || pr.Number <= 0 {
				continue
			}
			pr.HTMLURL = strings.TrimRight(p.Repository.HTMLURL, "/") + "/pull/" + strconv.Itoa(pr.Number)
			if ev, ok := w.webhookCheck(pr, cr.HeadSHA, cr.Name, cr.Conclusion, cr.CompletedAt); ok {
				out = append(out, ev)
			}
		}
		if len(cr.PullRequests) == 0 { // a fork's PR is not listed: find it by head
			if ev, ok := w.webhookCheck(whPR{HTMLURL: w.urlForHead(cr.HeadSHA)}, cr.HeadSHA, cr.Name, cr.Conclusion, cr.CompletedAt); ok {
				out = append(out, ev)
			}
		}
		return out, nil
	case "status":
		var p struct {
			SHA       string    `json:"sha"`
			Context   string    `json:"context"`
			State     string    `json:"state"`
			UpdatedAt time.Time `json:"updated_at"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		if !statusCompleted(p.State) {
			return nil, nil
		}
		if ev, ok := w.webhookCheck(whPR{HTMLURL: w.urlForHead(p.SHA)}, p.SHA, p.Context, p.State, p.UpdatedAt); ok {
			return []Event{ev}, nil
		}
		return nil, nil
	case "pull_request":
		var p struct {
			Action      string `json:"action"`
			PullRequest whPR   `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return w.webhookPullRequest(p.Action, p.PullRequest), nil
	case "pull_request_review":
		var p struct {
			Action string `json:"action"`
			Review struct {
				ID          int64     `json:"id"`
				State       string    `json:"state"`
				SubmittedAt time.Time `json:"submitted_at"`
				User        struct {
					Login string `json:"login"`
				} `json:"user"`
			} `json:"review"`
			PullRequest whPR `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		if p.Action != "submitted" {
			return nil, nil
		}
		m, st, ok := w.managedFor(p.PullRequest.HTMLURL)
		if !ok {
			return nil, nil
		}
		w.notePR(st, p.PullRequest)
		base := w.lockedBase(st, m)
		if ev, ok := w.reviewEvent(st, m, base, p.Review.ID, p.Review.User.Login, p.Review.State, p.Review.SubmittedAt); ok {
			return []Event{ev}, nil
		}
		return nil, nil
	}
	return nil, nil // ping and every other event
}

// lockedBase is baseEvent under w.mu, with the webhook source.
func (w *Watcher) lockedBase(st *prState, m Managed) Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.baseEvent(st, m, SourceWebhook)
}

// notePR records what a delivery says about the PR's head.
func (w *Watcher) notePR(st *prState, pr whPR) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if pr.Head.SHA != "" {
		st.headSHA = pr.Head.SHA
	}
	if pr.Head.Ref != "" {
		st.branch = pr.Head.Ref
	}
	if pr.Number > 0 {
		st.number = pr.Number
	}
}

// webhookCheck builds a CheckCompleted event for the managed PR pr names
// (its head as the delivery reports it, when it does).
func (w *Watcher) webhookCheck(pr whPR, headSHA, name, conclusion string, completedAt time.Time) (Event, bool) {
	if pr.HTMLURL == "" {
		return Event{}, false
	}
	m, st, ok := w.managedFor(pr.HTMLURL)
	if !ok {
		return Event{}, false
	}
	if pr.Head.SHA == headSHA { // the run is on the PR's current head
		w.notePR(st, pr)
	}
	base := w.lockedBase(st, m)
	base.HeadSHA = headSHA
	return w.checkEvent(st, m, base, name, conclusion, completedAt)
}

func (w *Watcher) webhookPullRequest(action string, pr whPR) []Event {
	m, st, ok := w.managedFor(pr.HTMLURL)
	if !ok {
		return nil
	}
	switch action {
	case "closed":
		w.mu.Lock()
		st.closed = true
		w.mu.Unlock()
		return nil
	case "reopened":
		w.mu.Lock()
		st.closed = false
		w.mu.Unlock()
	case "synchronize":
	default:
		return nil
	}
	w.notePR(st, pr)
	if !w.kinds()[Synchronized] || pr.Head.SHA == "" {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if st.emittedHead == pr.Head.SHA {
		return nil
	}
	st.emittedHead = pr.Head.SHA
	ev := w.baseEvent(st, m, SourceWebhook)
	ev.Kind = Synchronized
	return []Event{ev}
}
