// Package webhook receives Linear and GitHub issue webhooks and applies them
// to the local tracker store (#131) as targeted refreshes, so a change
// reaches the board in seconds instead of at the next poll.
//
// Every delivery must carry a valid HMAC-SHA256 signature made with the
// configured secret; anything else is dropped. A Linear delivery must also be
// recent (webhookTimestamp within ReplayWindow), which stops a captured
// delivery from being replayed later. A delivery only names issues; the store
// re-reads each one from the tracker by ID, so a payload never writes issue
// data directly.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/metrics"
)

// MaxBody bounds a delivery's size. GitHub caps payloads at 25 MB but issue
// events are a few kilobytes; anything larger is not an issue event.
const MaxBody = 1 << 20

// ReplayWindow is how far a Linear delivery's webhookTimestamp may be from
// now, as Linear's own guide recommends.
const ReplayWindow = time.Minute

// maxPending bounds the issues waiting to be refreshed. Past it the queue is
// dropped for one full pull, which covers every issue at once.
const maxPending = 500

// Store is what a delivery is applied to: the tracker store.
type Store interface {
	// RefreshIssues re-reads issues by ID and returns the ones it does not
	// hold.
	RefreshIssues(ctx context.Context, ids []string) (unknown []string, err error)
	// Forget drops an issue that was deleted or moved away.
	Forget(id string)
	// RequestSync asks for a full pull soon.
	RequestSync()
}

// Config configures a Handler.
type Config struct {
	// Tracker is "linear" or "github".
	Tracker string
	// Secret is the webhook signing secret.
	Secret string
	// Repo is the GitHub owner/repo (tracker.project_slug); a delivery for
	// another repository is ignored.
	Repo string
	// Store receives the deliveries.
	Store Store
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
}

// Handler serves POST /api/v1/webhooks/{tracker}. Run applies what it
// accepts.
type Handler struct {
	cfg Config
	now func() time.Time

	mu      sync.Mutex
	refresh map[string]struct{}
	forget  map[string]struct{}
	resync  bool
	wake    chan struct{}
	// lastWarn is when a dropped delivery was last logged.
	lastWarn time.Time
}

// New returns a Handler. It fails without a secret: unsigned deliveries are
// never accepted.
func New(cfg Config) (*Handler, error) {
	if cfg.Tracker != "linear" && cfg.Tracker != "github" {
		return nil, fmt.Errorf("webhook: tracker %q has no webhooks", cfg.Tracker)
	}
	if cfg.Secret == "" {
		return nil, errors.New("webhook: a signing secret is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("webhook: no store")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	metrics.PreinitWebhooks(cfg.Tracker)
	return &Handler{
		cfg:     cfg,
		now:     now,
		refresh: map[string]struct{}{},
		forget:  map[string]struct{}{},
		wake:    make(chan struct{}, 1),
	}, nil
}

// Tracker is the tracker kind the handler accepts deliveries for.
func (h *Handler) Tracker() string { return h.cfg.Tracker }

// delivery is what a payload asks for.
type delivery struct {
	refresh []string
	forget  []string
}

func (d delivery) empty() bool { return len(d.refresh) == 0 && len(d.forget) == 0 }

// ServeHTTP verifies and parses one delivery and queues it. It answers 202
// for an accepted or ignored delivery, 401 for a bad signature or a stale
// Linear delivery, 400 for a payload it cannot read.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil || len(body) > MaxBody {
		h.reject(w, metrics.WebhookMalformed, http.StatusRequestEntityTooLarge, "payload too large or unreadable")
		return
	}
	var d delivery
	switch h.cfg.Tracker {
	case "github":
		if !validSignature(h.cfg.Secret, body, strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")) {
			h.reject(w, metrics.WebhookBadSignature, http.StatusUnauthorized, "bad signature")
			return
		}
		d, err = parseGitHub(r.Header.Get("X-GitHub-Event"), body, h.cfg.Repo)
	default:
		if !validSignature(h.cfg.Secret, body, r.Header.Get("Linear-Signature")) {
			h.reject(w, metrics.WebhookBadSignature, http.StatusUnauthorized, "bad signature")
			return
		}
		var stale bool
		d, stale, err = parseLinear(body, h.now())
		if stale {
			h.reject(w, metrics.WebhookStale, http.StatusUnauthorized, "delivery outside the replay window")
			return
		}
	}
	if err != nil {
		h.reject(w, metrics.WebhookMalformed, http.StatusBadRequest, "unreadable payload")
		return
	}
	if d.empty() {
		metrics.Webhook(h.cfg.Tracker, metrics.WebhookIgnored)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	h.enqueue(d)
	metrics.Webhook(h.cfg.Tracker, metrics.WebhookAccepted)
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) reject(w http.ResponseWriter, result string, status int, msg string) {
	metrics.Webhook(h.cfg.Tracker, result)
	// The route needs no token, so anyone who can reach it can send junk:
	// warn at most once a minute (the counter has every one).
	h.mu.Lock()
	warn := h.now().Sub(h.lastWarn) >= time.Minute
	if warn {
		h.lastWarn = h.now()
	}
	h.mu.Unlock()
	if warn {
		slog.Warn("tracker webhook: delivery dropped (further drops this minute are only counted)",
			"tracker", h.cfg.Tracker, "result", result)
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

func (h *Handler) enqueue(d delivery) {
	h.mu.Lock()
	for _, id := range d.forget {
		delete(h.refresh, id)
		h.forget[id] = struct{}{}
	}
	for _, id := range d.refresh {
		if _, gone := h.forget[id]; !gone {
			h.refresh[id] = struct{}{}
		}
	}
	if len(h.refresh)+len(h.forget) > maxPending {
		// A flood: one full pull covers every issue it named, removals
		// included (a deleted issue is not in the pull).
		clear(h.refresh)
		clear(h.forget)
		h.resync = true
	}
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// refreshTimeout bounds one batch of by-ID reads.
const refreshTimeout = 30 * time.Second

// Run applies queued deliveries until ctx ends.
func (h *Handler) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.wake:
		}
		h.drain(ctx)
	}
}

// drain applies everything queued so far.
func (h *Handler) drain(ctx context.Context) {
	h.mu.Lock()
	refresh := make([]string, 0, len(h.refresh))
	for id := range h.refresh {
		refresh = append(refresh, id)
	}
	forget := make([]string, 0, len(h.forget))
	for id := range h.forget {
		forget = append(forget, id)
	}
	resync := h.resync
	clear(h.refresh)
	clear(h.forget)
	h.resync = false
	h.mu.Unlock()

	for _, id := range forget {
		h.cfg.Store.Forget(id)
	}
	if len(refresh) > 0 {
		rctx, cancel := context.WithTimeout(ctx, refreshTimeout)
		unknown, err := h.cfg.Store.RefreshIssues(rctx, refresh)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("tracker webhook: refresh failed, pulling instead", "tracker", h.cfg.Tracker, "error", err)
			resync = true
		}
		if len(unknown) > 0 {
			// The adapter cannot check its scope by ID, so only a pull can
			// tell whether a new issue belongs to the tracked project.
			resync = true
		}
	}
	if resync {
		h.cfg.Store.RequestSync()
	}
}

// parseGitHub reads an issues event. Comments, pull requests, other
// repositories and other events name nothing to refresh.
func parseGitHub(event string, body []byte, repo string) (delivery, error) {
	if event != "issues" {
		return delivery{}, nil // ping, issue_comment, label, …
	}
	var p struct {
		Action string `json:"action"`
		Issue  *struct {
			Number      int              `json:"number"`
			PullRequest *json.RawMessage `json:"pull_request"`
		} `json:"issue"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return delivery{}, err
	}
	if p.Issue == nil || p.Issue.Number <= 0 {
		return delivery{}, errors.New("webhook: issues event without an issue")
	}
	if p.Issue.PullRequest != nil || !strings.EqualFold(p.Repository.FullName, repo) {
		return delivery{}, nil
	}
	id := strconv.Itoa(p.Issue.Number)
	switch p.Action {
	case "deleted", "transferred":
		return delivery{forget: []string{id}}, nil
	}
	return delivery{refresh: []string{id}}, nil
}

// parseLinear reads an Issue event. stale reports a delivery outside the
// replay window. Comments, labels and other entities name nothing to
// refresh.
func parseLinear(body []byte, now time.Time) (d delivery, stale bool, err error) {
	var p struct {
		Action           string `json:"action"`
		Type             string `json:"type"`
		WebhookTimestamp int64  `json:"webhookTimestamp"`
		Data             struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return delivery{}, false, err
	}
	sent := time.UnixMilli(p.WebhookTimestamp)
	if p.WebhookTimestamp == 0 || now.Sub(sent).Abs() > ReplayWindow {
		return delivery{}, true, nil
	}
	if p.Type != "Issue" {
		return delivery{}, false, nil
	}
	if p.Data.ID == "" {
		return delivery{}, false, errors.New("webhook: Issue event without an id")
	}
	if p.Action == "remove" {
		return delivery{forget: []string{p.Data.ID}}, false, nil
	}
	return delivery{refresh: []string{p.Data.ID}}, false, nil
}
