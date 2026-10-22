package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Hub notification settings (#229): per project and per event kind, which
// channels a merged-feed event goes to; per-project snooze; and the digest
// interval that batches low-priority kinds. The settings live in
// StateDir/notifications.json (0600, schema at NotificationsSchemaURL) and
// are hub-side state: never orchestrator state, never a member's.

// NotificationsFileName is the settings file inside the hub's StateDir.
const NotificationsFileName = "notifications.json"

// NotificationsSchemaURL is the published JSON Schema of the settings file
// (site/public/schema/notifications.schema.json).
const NotificationsSchemaURL = "https://itervox.dev/schema/notifications.schema.json"

// notificationsVersion versions the settings file.
const notificationsVersion = 1

// Notification kinds: what an event of the merged feed means to a person.
// Each maps from a #213 feed kind (NotificationKindOf). There is no budget
// kind: a #136 budget pause emits no feed event yet.
const (
	KindInputRequired     = "input_required"
	KindGuardrailApproval = "guardrail_approval"
	KindRunFailed         = "run_failed"
	KindReviewVerdict     = "review_verdict"
	KindPRMerged          = "pr_merged"
	KindLessonProposed    = "lesson_proposed"
	KindDispatchPaused    = "dispatch_paused"
)

// NotificationKinds lists every notification kind, in display order.
var NotificationKinds = []string{
	KindInputRequired, KindGuardrailApproval, KindRunFailed, KindReviewVerdict,
	KindPRMerged, KindLessonProposed, KindDispatchPaused,
}

// urgentKinds are delivered at once and never batched into a digest.
var urgentKinds = map[string]bool{KindInputRequired: true, KindGuardrailApproval: true}

// defaultOnKinds go to every enabled channel unless a project says
// otherwise; every other kind is off by default.
var defaultOnKinds = map[string]bool{KindInputRequired: true, KindGuardrailApproval: true, KindRunFailed: true}

// IsUrgentKind reports whether kind is delivered at once, never batched.
func IsUrgentKind(kind string) bool { return urgentKinds[kind] }

// Notification channels: the channel registry. Each channel is a Channel
// implementation registered with the hub (desktop is built in; Slack #230
// and Web Push #231 register theirs through Config.Channels).
const (
	ChannelPush    = "push"
	ChannelSlack   = "slack"
	ChannelDesktop = "desktop"
)

// ChannelNames lists every channel the settings may route to, in order.
var ChannelNames = []string{ChannelPush, ChannelSlack, ChannelDesktop}

// Digest interval bounds, in minutes.
const (
	DefaultDigestIntervalMinutes = 15
	maxDigestIntervalMinutes     = 24 * 60
)

// maxNotificationsFileBytes caps the settings file and a PUT body.
const maxNotificationsFileBytes = 256 << 10

// NotificationSettings is the content of notifications.json.
type NotificationSettings struct {
	Schema  string `json:"$schema,omitempty"`
	Version int    `json:"version"`
	// DigestIntervalMinutes is the shortest gap between two digests of
	// low-priority kinds (default 15).
	DigestIntervalMinutes int `json:"digest_interval_minutes"`
	// Channels switches a channel off for every project.
	Channels map[string]ChannelSettings `json:"channels"`
	// Defaults is the route of each kind for a project without its own.
	Defaults map[string][]string `json:"defaults"`
	// Projects holds per-project routes and snooze, keyed by hub project
	// name (the /hub/p/{project} segment).
	Projects map[string]ProjectNotificationSettings `json:"projects"`
}

// ChannelSettings is one channel's global switch.
type ChannelSettings struct {
	Enabled bool `json:"enabled"`
}

// ProjectNotificationSettings is one project's routes and snooze.
type ProjectNotificationSettings struct {
	// Routes overrides Defaults per kind; an empty list turns a kind off.
	Routes map[string][]string `json:"routes,omitempty"`
	// SnoozeUntil suppresses every notification of the project until then.
	SnoozeUntil *time.Time `json:"snooze_until,omitempty"`
}

// DefaultNotificationSettings is the built-in configuration: input
// required, guardrail approval and run failed go to every channel; the
// other kinds are off.
func DefaultNotificationSettings() NotificationSettings {
	s := NotificationSettings{}
	s.normalize()
	return s
}

// normalize fills what a file may leave out: the schema and version, the
// digest interval, every channel's switch (on) and every kind's default.
func (s *NotificationSettings) normalize() {
	s.Schema = NotificationsSchemaURL
	s.Version = notificationsVersion
	if s.DigestIntervalMinutes == 0 {
		s.DigestIntervalMinutes = DefaultDigestIntervalMinutes
	}
	if s.Channels == nil {
		s.Channels = map[string]ChannelSettings{}
	}
	for _, ch := range ChannelNames {
		if _, ok := s.Channels[ch]; !ok {
			s.Channels[ch] = ChannelSettings{Enabled: true}
		}
	}
	if s.Defaults == nil {
		s.Defaults = map[string][]string{}
	}
	for _, k := range NotificationKinds {
		if _, ok := s.Defaults[k]; !ok {
			s.Defaults[k] = []string{}
			if defaultOnKinds[k] {
				s.Defaults[k] = slices.Clone(ChannelNames)
			}
		}
	}
	for k, chs := range s.Defaults {
		s.Defaults[k] = orderedChannels(chs)
	}
	if s.Projects == nil {
		s.Projects = map[string]ProjectNotificationSettings{}
	}
	for name, p := range s.Projects {
		for k, chs := range p.Routes {
			p.Routes[k] = orderedChannels(chs)
		}
		s.Projects[name] = p
	}
}

// orderedChannels returns chs deduplicated, in ChannelNames order.
func orderedChannels(chs []string) []string {
	out := []string{}
	for _, ch := range ChannelNames {
		if slices.Contains(chs, ch) {
			out = append(out, ch)
		}
	}
	return out
}

// ParseNotificationSettings decodes and validates a settings document
// (unknown fields refused) and returns it normalized.
func ParseNotificationSettings(data []byte) (NotificationSettings, error) {
	if len(data) > maxNotificationsFileBytes {
		return NotificationSettings{}, errors.New("hub: notification settings are too large")
	}
	var s NotificationSettings
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return NotificationSettings{}, fmt.Errorf("hub: notification settings: %w", err)
	}
	if err := s.validate(); err != nil {
		return NotificationSettings{}, err
	}
	s.normalize()
	return s, nil
}

func (s *NotificationSettings) validate() error {
	if s.Schema != "" && s.Schema != NotificationsSchemaURL {
		return fmt.Errorf("hub: notification settings: $schema must be %s", NotificationsSchemaURL)
	}
	if s.Version != 0 && s.Version != notificationsVersion {
		return fmt.Errorf("hub: notification settings: unsupported version %d", s.Version)
	}
	if s.DigestIntervalMinutes < 0 || s.DigestIntervalMinutes > maxDigestIntervalMinutes {
		return fmt.Errorf("hub: notification settings: digest_interval_minutes must be between 1 and %d", maxDigestIntervalMinutes)
	}
	for ch := range s.Channels {
		if !slices.Contains(ChannelNames, ch) {
			return fmt.Errorf("hub: notification settings: unknown channel %q", ch)
		}
	}
	if err := validateRoutes("defaults", s.Defaults); err != nil {
		return err
	}
	for name, p := range s.Projects {
		if strings.TrimSpace(name) == "" || len(name) > 128 {
			return errors.New("hub: notification settings: a project name is empty or too long")
		}
		if err := validateRoutes("projects."+name+".routes", p.Routes); err != nil {
			return err
		}
	}
	return nil
}

func validateRoutes(where string, routes map[string][]string) error {
	for kind, chs := range routes {
		if !slices.Contains(NotificationKinds, kind) {
			return fmt.Errorf("hub: notification settings: %s: unknown kind %q", where, kind)
		}
		for _, ch := range chs {
			if !slices.Contains(ChannelNames, ch) {
				return fmt.Errorf("hub: notification settings: %s.%s: unknown channel %q", where, kind, ch)
			}
		}
	}
	return nil
}

// Snoozed reports whether project's notifications are suppressed at now.
func (s *NotificationSettings) Snoozed(project string, now time.Time) bool {
	p, ok := s.Projects[project]
	return ok && p.SnoozeUntil != nil && now.Before(*p.SnoozeUntil)
}

// Route is the routing decision every channel calls: the channels an event
// of kind from project goes to at now — the project's route for kind (or
// the default), minus channels switched off, and none while the project is
// snoozed or for an unknown kind. The result is in ChannelNames order.
func (s *NotificationSettings) Route(project, kind string, now time.Time) []string {
	if !slices.Contains(NotificationKinds, kind) || s.Snoozed(project, now) {
		return nil
	}
	chs, ok := s.Projects[project].Routes[kind]
	if !ok {
		chs = s.Defaults[kind]
	}
	var out []string
	for _, ch := range orderedChannels(chs) {
		if c, ok := s.Channels[ch]; ok && c.Enabled {
			out = append(out, ch)
		}
	}
	return out
}

// clone deep-copies s.
func (s NotificationSettings) clone() NotificationSettings {
	out := s
	out.Channels = maps.Clone(s.Channels)
	out.Defaults = make(map[string][]string, len(s.Defaults))
	for k, v := range s.Defaults {
		out.Defaults[k] = slices.Clone(v)
	}
	out.Projects = make(map[string]ProjectNotificationSettings, len(s.Projects))
	for name, p := range s.Projects {
		cp := ProjectNotificationSettings{}
		if p.SnoozeUntil != nil {
			t := *p.SnoozeUntil
			cp.SnoozeUntil = &t
		}
		if p.Routes != nil {
			cp.Routes = make(map[string][]string, len(p.Routes))
			for k, v := range p.Routes {
				cp.Routes[k] = slices.Clone(v)
			}
		}
		out.Projects[name] = cp
	}
	return out
}

// feedInputPayload reads the guardrail marker of an input_required event.
type feedInputPayload struct {
	Kind string `json:"kind"`
}

// NotificationKindOf maps a merged-feed event to its notification kind;
// "" when the event notifies nobody (run_started, input_answered, ...).
// An input_required event whose payload kind is "guardrail_approval" is a
// guardrail approval.
func NotificationKindOf(ev Event) string {
	switch ev.Kind {
	case "input_required":
		var p feedInputPayload
		if len(ev.Payload) > 0 && json.Unmarshal(ev.Payload, &p) == nil && p.Kind == KindGuardrailApproval {
			return KindGuardrailApproval
		}
		return KindInputRequired
	case "run_failed":
		return KindRunFailed
	case "review_verdict":
		return KindReviewVerdict
	case "pr_merged":
		return KindPRMerged
	case "lesson_proposed":
		return KindLessonProposed
	case "dispatch_paused":
		return KindDispatchPaused
	}
	return ""
}

// notificationKindsOfFeedKind lists the notification kinds a feed kind can
// map to (Notifier.Enabled sees the kind only, not the payload).
func notificationKindsOfFeedKind(feedKind string) []string {
	if feedKind == "input_required" {
		return []string{KindInputRequired, KindGuardrailApproval}
	}
	if k := NotificationKindOf(Event{Kind: feedKind}); k != "" {
		return []string{k}
	}
	return nil
}
