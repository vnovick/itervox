package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/logging"
)

// Web Push (#231): the hub's built-in "push" channel. A phone (or any
// browser) subscribes with POST /hub/api/v1/push/subscribe and an operator
// token; the subscription is kept per device, with a name, in
// StateDir/push_subscriptions.json (0600). The channel notifier routes
// through NotificationSettings.Route like every channel; this file only
// delivers: a minimal payload (kind, project, issue, a short redacted title
// and a deep link — never agent output), encrypted per RFC 8291 and signed
// with the hub's VAPID key (push_vapid.go), POSTed to each device's push
// service. A device whose subscription the push service reports gone (404,
// 410) is removed; so is one whose hub token was revoked or expired.

// PushSubscriptionsFileName is the push device list inside StateDir.
const PushSubscriptionsFileName = "push_subscriptions.json"

const pushSubscriptionsVersion = 1

// Push limits.
const (
	maxPushDevices      = 50
	maxPushEndpointLen  = 2048
	maxPushDeviceName   = 48
	maxPushTitleRunes   = 80
	maxPushFieldRunes   = 64
	pushTTLSeconds      = "86400"
	pushRequestTimeout  = 15 * time.Second
	maxPushReplyDrained = 4 << 10
)

// DefaultPushServices are the push service host names (and their
// subdomains) a subscription endpoint may point at: Chrome and Android,
// Firefox, Safari and iOS, and Edge. Anything else is refused, so a
// subscription cannot turn the hub into a request forwarder.
var DefaultPushServices = []string{
	"fcm.googleapis.com", "android.googleapis.com",
	"push.services.mozilla.com",
	"push.apple.com",
	"notify.windows.com",
}

// pushDevice is one subscribed browser. Endpoint is a capability URL and
// the keys are the browser's: none of them is ever listed or logged.
type pushDevice struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Endpoint string    `json:"endpoint"`
	P256dh   string    `json:"p256dh"`
	Auth     string    `json:"auth"`
	TokenID  string    `json:"tokenId"`
	Created  time.Time `json:"created"`
}

// PushDeviceRow is one device in GET /hub/api/v1/push/devices: its name and
// push service only.
type PushDeviceRow struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Service string    `json:"service"`
	TokenID string    `json:"tokenId"`
	Created time.Time `json:"created"`
}

func (d pushDevice) row() PushDeviceRow {
	host := ""
	if u, err := url.Parse(d.Endpoint); err == nil {
		host = u.Hostname()
	}
	return PushDeviceRow{ID: d.ID, Name: d.Name, Service: host, TokenID: d.TokenID, Created: d.Created}
}

type pushFile struct {
	Version int          `json:"version"`
	Devices []pushDevice `json:"devices"`
}

// pushStore is the device list, in memory and in StateDir.
type pushStore struct {
	path    string
	mu      sync.Mutex
	devices []pushDevice
}

func loadPushStore(path string) *pushStore {
	s := &pushStore{path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		slog.Warn("hub: push: cannot read the device list", "path", path, "error", err)
	default:
		var f pushFile
		if jerr := json.Unmarshal(data, &f); jerr != nil || f.Version != pushSubscriptionsVersion {
			slog.Warn("hub: push: the device list does not parse; starting empty", "path", path)
			break
		}
		s.devices = f.Devices
	}
	return s
}

// saveLocked writes the list 0600; s.mu is held.
func (s *pushStore) saveLocked() error {
	data, err := json.MarshalIndent(pushFile{Version: pushSubscriptionsVersion, Devices: nonNilDevices(s.devices)}, "", "  ")
	if err != nil {
		return fmt.Errorf("hub: push: %w", err)
	}
	if err := atomicfs.WriteFile(s.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("hub: push: save the device list: %w", err)
	}
	return nil
}

func nonNilDevices(d []pushDevice) []pushDevice {
	if d == nil {
		return []pushDevice{}
	}
	return d
}

func (s *pushStore) list() []pushDevice {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.devices)
}

// upsert adds d, or replaces the device with the same endpoint (keeping
// its id): a browser re-subscribing is the same device.
func (s *pushStore) upsert(d pushDevice) (pushDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := slices.IndexFunc(s.devices, func(x pushDevice) bool { return x.Endpoint == d.Endpoint }); i >= 0 {
		d.ID, d.Created = s.devices[i].ID, s.devices[i].Created
		s.devices[i] = d
	} else {
		if len(s.devices) >= maxPushDevices {
			return pushDevice{}, errTooManyPushDevices
		}
		s.devices = append(s.devices, d)
	}
	return d, s.saveLocked()
}

var errTooManyPushDevices = fmt.Errorf("hub: push: at most %d devices", maxPushDevices)

// remove deletes every device keep rejects and reports how many went.
func (s *pushStore) remove(drop func(pushDevice) bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.devices)
	s.devices = slices.DeleteFunc(s.devices, drop)
	if removed := n - len(s.devices); removed > 0 {
		return removed, s.saveLocked()
	}
	return 0, nil
}

// pushChannel is the "push" Channel.
type pushChannel struct {
	keys    *vapidKeys
	store   *pushStore
	tokens  *apitokens.Store
	client  *http.Client
	allowed func(*url.URL) bool
	subject string
	now     func() time.Time
}

func newPushChannel(cfg Config, keys *vapidKeys, store *pushStore, tokens *apitokens.Store) *pushChannel {
	client := &http.Client{Timeout: pushRequestTimeout}
	if cfg.PushHTTPClient != nil {
		c := *cfg.PushHTTPClient
		client = &c
	}
	// A push service never redirects; following one would send the
	// encrypted payload and VAPID token somewhere else.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	allowed := cfg.PushEndpointAllowed
	if allowed == nil {
		allowed = defaultPushEndpointAllowed
	}
	subject := cfg.PushSubject
	if subject == "" {
		subject = DefaultVAPIDSubject
	}
	return &pushChannel{keys: keys, store: store, tokens: tokens, client: client, allowed: allowed, subject: subject, now: time.Now}
}

// defaultPushEndpointAllowed accepts https endpoints on DefaultPushServices.
func defaultPushEndpointAllowed(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, svc := range DefaultPushServices {
		if host == svc || strings.HasSuffix(host, "."+svc) {
			return true
		}
	}
	return false
}

// Name implements Channel.
func (*pushChannel) Name() string { return ChannelPush }

// errNoPushDevices answers a test notification with nothing subscribed.
var errNoPushDevices = errors.New("hub: push: no device is subscribed")

// Send implements Channel: one encrypted push per subscribed device. A
// device whose push service answers 404/410, or whose hub token is gone,
// is removed. Any other failure that may pass (429, 5xx, a network error)
// is returned, so an urgent notification is retried (every device may see
// it again: delivery is at least once).
func (p *pushChannel) Send(ctx context.Context, n Notification) error {
	devices, err := p.liveDevices()
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		if n.Kind == NotificationTest {
			return errNoPushDevices
		}
		return nil
	}
	payload, err := json.Marshal(pushPayloadOf(n))
	if err != nil {
		return fmt.Errorf("hub: push: %w", err)
	}
	var errs []error
	for _, d := range devices {
		gone, err := p.sendOne(ctx, d, payload, n)
		switch {
		case gone:
			_, _ = p.store.remove(func(x pushDevice) bool { return x.ID == d.ID })
			slog.Info("hub: push: the push service dropped a device's subscription; removed it", "device", d.ID, "name", d.Name)
		case err != nil:
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// liveDevices lists the devices whose hub token still authenticates and
// removes the others (a revoked or expired token takes its pushes along).
func (p *pushChannel) liveDevices() ([]pushDevice, error) {
	tokens, err := p.tokens.List()
	if err != nil {
		return nil, fmt.Errorf("hub: push: the hub token store: %w", err)
	}
	now := p.now()
	live := map[string]bool{}
	for _, t := range tokens {
		if !t.Expired(now) {
			live[t.ID] = true
		}
	}
	if removed, err := p.store.remove(func(d pushDevice) bool { return !live[d.TokenID] }); removed > 0 {
		slog.Info("hub: push: removed devices whose hub token is revoked or expired", "count", removed, "error", err)
	}
	return p.store.list(), nil
}

// sendOne POSTs payload to one device. gone reports a subscription the
// push service no longer knows.
func (p *pushChannel) sendOne(ctx context.Context, d pushDevice, payload []byte, n Notification) (gone bool, err error) {
	endpoint, err := url.Parse(d.Endpoint)
	if err != nil || !p.allowed(endpoint) {
		return true, nil // no longer an endpoint this hub sends to
	}
	uaPublic, err1 := decodeB64URL(d.P256dh)
	auth, err2 := decodeB64URL(d.Auth)
	if err1 != nil || err2 != nil {
		return true, nil
	}
	body, err := encryptPush(payload, uaPublic, auth)
	if err != nil {
		return true, nil
	}
	authz, err := p.keys.authorization(endpoint, p.subject, p.now())
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Endpoint, bytes.NewReader(body))
	if err != nil {
		return true, nil
	}
	req.Header.Set("Authorization", authz)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", pushTTLSeconds)
	req.Header.Set("Urgency", map[bool]string{true: "high", false: "normal"}[n.Urgent])
	if topic := pushTopic(n); topic != "" {
		req.Header.Set("Topic", topic)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// The error names the endpoint URL (a capability): log the host only.
		slog.Warn("hub: push: the push service did not answer", "device", d.ID, "service", endpoint.Hostname())
		return false, fmt.Errorf("hub: push: device %s: the push service did not answer", d.ID)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxPushReplyDrained))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return true, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500:
		return false, fmt.Errorf("hub: push: device %s: the push service answered %d", d.ID, resp.StatusCode)
	default:
		// A refusal a retry cannot fix (bad VAPID, payload, ...): logged,
		// not retried, so one device never stalls the channel.
		slog.Warn("hub: push: the push service refused a notification", "device", d.ID, "service", endpoint.Hostname(), "status", resp.StatusCode)
		return false, nil
	}
}

// PushPayload is what a push carries (encrypted): the minimum a phone needs
// to show the notification and open the right page. Never agent output,
// prompt text, a digest's items or any free text beyond the short title.
type PushPayload struct {
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
	Issue   string `json:"issue,omitempty"`
	Title   string `json:"title"`
	// URL is a hub-relative deep link into the mobile layout.
	URL string `json:"url"`
}

// pushPayloadOf builds the payload of n: every text field redacted and
// bounded; the link points at the mobile issue page (or /hub/m).
func pushPayloadOf(n Notification) PushPayload {
	p := PushPayload{
		Kind:    clampRunes(pushWord(n.Kind), maxPushFieldRunes),
		Project: clampRunes(logging.RedactString(printable(n.Project)), maxPushFieldRunes),
		Issue:   clampRunes(logging.RedactString(printable(n.Identifier)), maxPushFieldRunes),
		Title:   clampRunes(logging.RedactString(printable(n.Title)), maxPushTitleRunes),
		URL:     "/hub/m",
	}
	if p.Project != "" && p.Issue != "" && p.Project == n.Project && p.Issue == n.Identifier {
		p.URL = "/hub/p/" + url.PathEscape(n.Project) + "/m/" + url.PathEscape(n.Identifier)
	}
	return p
}

// pushWord keeps a kind that is a short enum word, else "notification".
func pushWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyz_") == "" {
		return s
	}
	return "notification"
}

// printable collapses control characters and newlines to spaces.
func printable(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }), " ")
}

// clampRunes cuts s to max runes, ending with "…" when cut.
func clampRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max-1])) + "…"
}

// pushTopic collapses repeated pushes about one issue on the push service
// (RFC 8030 Topic: at most 32 base64url characters).
func pushTopic(n Notification) string {
	if n.Project == "" || n.Identifier == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(n.Project + "\x00" + n.Identifier))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// decodeB64URL decodes the base64url (padded or not) a browser's
// PushSubscription.toJSON() gives.
func decodeB64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// newPushDeviceID is a short random id.
func newPushDeviceID() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("hub: push: %w", err)
	}
	return "pd_" + hex.EncodeToString(buf), nil
}
