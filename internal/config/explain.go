package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/vnovick/itervox/internal/workflow"
)

// Setting is one effective configuration value and where it came from, as
// `itervox config show` prints it (#129).
type Setting struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
	// Layer is LayerDefault, LayerUser, LayerProject or LayerWorkflow.
	Layer string `json:"layer"`
	// Path is the settings file for the user and project layers, and the
	// variable for the environment layer.
	Path string `json:"path,omitempty"`
	// Note explains a value that is not what a layer set, or a key Itervox
	// does not read.
	Note string `json:"note,omitempty"`
	// Unread is set for a key a layer sets that Itervox does not read.
	Unread bool `json:"unread,omitempty"`
}

// redactedValue stands in for a secret in Explain's output.
const redactedValue = "(set, hidden)"

var secretKey = regexp.MustCompile(`(?i)(^|[._])(api_key|api_token|token|secret|password)$`)

// explaining is the recorder of the one Explain in progress, nil otherwise.
// Loads running alongside it are unaffected: the recorder only notes reads
// from maps it registered.
var (
	explaining atomic.Pointer[settingsRecorder]
	explainMu  sync.Mutex
)

// settingsRecorder notes every value the config parser reads, by key path.
type settingsRecorder struct {
	mu     sync.Mutex
	paths  map[uintptr]string // registered map → its key path
	keep   []map[string]any   // holds registered maps so no address is reused
	values map[string]any
}

func mapID(m map[string]any) uintptr {
	if m == nil {
		return 0
	}
	return uintptr(reflect.ValueOf(m).UnsafePointer())
}

// noteSection registers child as the map at parent's path + key.
func noteSection(parent map[string]any, key string, child map[string]any) {
	r := explaining.Load()
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	base, ok := r.paths[mapID(parent)]
	if !ok {
		return
	}
	r.paths[mapID(child)] = joinKey(base, key)
	r.keep = append(r.keep, child)
}

// noteSetting records the value parsed for key in m.
func noteSetting(m map[string]any, key string, v any) {
	r := explaining.Load()
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	base, ok := r.paths[mapID(m)]
	if !ok {
		return
	}
	r.values[joinKey(base, key)] = v
}

func joinKey(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

// Explain loads the workflow with its settings layers and returns the
// settings: every value the configuration reads, plus every key a settings
// layer or the agent, polling, server and workspace sections of WORKFLOW.md
// set, each with the layer it came from (LayerDefault when none did, or
// LayerEnvironment for a server bind override). Secrets are hidden.
func Explain(workflowPath string) ([]Setting, error) {
	wf, err := workflow.Load(workflowPath)
	if err != nil {
		return nil, err
	}
	layers, err := readSettingsLayers(workflowPath)
	if err != nil {
		return nil, err
	}
	merged, sources := mergeLayers(wf.Config, layers)
	wf.Config = merged

	explainMu.Lock()
	r := &settingsRecorder{paths: map[uintptr]string{mapID(merged): ""}, values: map[string]any{}}
	explaining.Store(r)
	cfg, err := fromWorkflow(wf, workflowPath)
	explaining.Store(nil)
	explainMu.Unlock()
	if err != nil {
		return nil, err
	}

	values := maps.Clone(r.values)
	// Values the parser post-processes after reading them are taken from
	// the loaded config, so the listed value is the one in effect.
	for key, get := range effectiveValues {
		values[key] = get(cfg)
	}
	// A key a layer sets that nothing read (a typo, a removed setting) is
	// listed too, with what the layer set.
	for key := range sources {
		if !inLayeredSection(key) || coveredBy(values, key) {
			continue
		}
		values[key] = lookupPath(merged, key)
	}

	out := make([]Setting, 0, len(values))
	for key, v := range values {
		s := Setting{Key: key, Value: v, Layer: LayerDefault}
		if src, ok := sourceFor(sources, key); ok {
			s.Layer, s.Path = src.layer, src.path
			raw := lookupPath(merged, key)
			switch {
			case !coveredByRead(r.values, key):
				s.Note, s.Unread = "Itervox does not read this key", true
			case ValueKind(raw) != ValueKind(v):
				// The parser fell back to the default: the layer's value
				// is not in effect.
				s.Note = fmt.Sprintf("%s value %s ignored: not a %s", s.Layer, formatJSON(raw), ValueKind(v))
				s.Layer, s.Path = LayerDefault, ""
			}
		}
		if env, ok := envSourceFor(cfg, key); ok {
			s.Layer, s.Path = LayerEnvironment, env
		}
		s.Value = redactSetting(key, s.Value)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// LayerEnvironment marks a value set by an environment variable (the server
// bind overrides, CORE-058); Setting.Path names the variable.
const LayerEnvironment = "environment"

// effectiveValues are read straight from the loaded config: the parser reads
// these keys by hand, or corrects the value after reading it.
var effectiveValues = map[string]func(*Config) any{
	"server.host": func(c *Config) any { return c.Server.Host },
	"server.port": func(c *Config) any {
		if c.Server.Port == nil {
			return nil
		}
		return *c.Server.Port
	},
	"server.allow_unauthenticated":              func(c *Config) any { return c.Server.AllowUnauthenticatedLAN },
	"server.allowed_hosts":                      func(c *Config) any { return c.Server.AllowedHosts },
	"dependencies.confidence_threshold":         func(c *Config) any { return c.Dependencies.ConfidenceThreshold },
	"dependencies.ordering":                     func(c *Config) any { return c.Dependencies.Ordering },
	"dependencies.escalate_blocked_after_hours": func(c *Config) any { return c.Dependencies.EscalateBlockedAfterHours },
	"dependencies.analysis_mode":                func(c *Config) any { return c.Dependencies.AnalysisMode },
	"hooks.timeout_ms":                          func(c *Config) any { return c.Hooks.TimeoutMs },
	"agent.ssh_strict_host_checking":            func(c *Config) any { return c.Agent.SSHStrictHostChecking },
}

// readByHand are keys the parser reads without a helper and whose value is
// listed as the layer set it: a structured setting, and deprecated aliases.
var readByHand = []string{
	"agent.backend_fallback",
	"server.allow_unauthenticated_lan",
	"dependencies.auto_analyze",
}

func envSourceFor(cfg *Config, key string) (string, bool) {
	switch {
	case key == "server.host" && cfg.Server.HostSource == EnvServerHost:
		return EnvServerHost, true
	case key == "server.port" && (cfg.Server.PortSource == EnvServerPort || cfg.Server.PortSource == EnvGenericPort):
		return cfg.Server.PortSource, true
	}
	return "", false
}

func inLayeredSection(key string) bool {
	section, _, _ := strings.Cut(key, ".")
	return slices.Contains(LayeredSections, section)
}

// coveredBy reports whether key, or a map it sits in, is listed.
func coveredBy(values map[string]any, key string) bool {
	for k := key; ; {
		if _, ok := values[k]; ok {
			return true
		}
		i := strings.LastIndexByte(k, '.')
		if i < 0 {
			return false
		}
		k = k[:i]
	}
}

func coveredByRead(read map[string]any, key string) bool {
	if coveredBy(read, key) {
		return true
	}
	if _, ok := effectiveValues[key]; ok {
		return true
	}
	for _, k := range readByHand {
		if key == k || strings.HasPrefix(key, k+".") {
			return true
		}
	}
	return false
}

// sourceFor is the layer that set key: its own entry, or for a map the
// highest layer that set one of its keys.
func sourceFor(sources map[string]settingsSource, key string) (settingsSource, bool) {
	if src, ok := sources[key]; ok {
		return src, true
	}
	var best settingsSource
	found := false
	for k, src := range sources {
		if strings.HasPrefix(k, key+".") && (!found || layerRank(src.layer) > layerRank(best.layer)) {
			best, found = src, true
		}
	}
	return best, found
}

func layerRank(layer string) int {
	switch layer {
	case LayerUser:
		return 1
	case LayerProject:
		return 2
	case LayerWorkflow:
		return 3
	}
	return 0
}

func lookupPath(m map[string]any, key string) any {
	var cur any = m
	for _, part := range strings.Split(key, ".") {
		mm, ok := asStringMap(cur)
		if !ok {
			return nil
		}
		cur = mm[part]
	}
	return cur
}

// ValueKind is a value's JSON kind: "boolean", "number", "string", "list",
// "map" or "null".
func ValueKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case int, int64, float64:
		return "number"
	case string:
		return "string"
	case []any, []string:
		return "list"
	case map[string]any, map[any]any, map[string]string:
		return "map"
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Struct, reflect.Map:
		return "map"
	case reflect.Slice, reflect.Array:
		return "list"
	}
	return fmt.Sprintf("%T", v)
}

func formatJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// redactSetting hides a secret key's value and the password in a URL value.
func redactSetting(key string, v any) any {
	if secretKey.MatchString(key) {
		if isZero(v) {
			return ""
		}
		return redactedValue
	}
	if s, ok := v.(string); ok && strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.User != nil {
			if _, hasPassword := u.User.Password(); hasPassword {
				u.User = url.UserPassword(u.User.Username(), "hidden")
				return u.String()
			}
		}
	}
	return v
}

func isZero(v any) bool {
	return v == nil || reflect.ValueOf(v).IsZero()
}
