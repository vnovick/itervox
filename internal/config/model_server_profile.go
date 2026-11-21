package config

import (
	"cmp"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
)

// A profile's model_server (agent.profiles.<name>.model_server): the base
// URL of a model server the profile's agent CLI talks to instead of its
// vendor's, without writing `ANTHROPIC_BASE_URL=…` into the command.
// Itervox sets the variable the profile's backend reads when it runs the
// agent (ModelServerEnvVar), gives the profile its own quota pool unless it
// sets one (AssignModelServerPools), and lengthens its timeouts (ProfileTimeouts).

// Timeouts a profile that uses a model server gets when it does not set its
// own. A local model reads a long prompt before it prints anything and then
// writes a few tokens a second, so the hosted-API defaults (30 s read, 5 min
// stall, 1 h run) kill healthy runs. Each is a floor over the agent-level
// value: a larger agent value is kept, and an agent-level 0 (turn and stall
// timeouts disabled) stays 0.
const (
	// ModelServerReadTimeoutMs: five minutes for the first output of a
	// turn, which follows the server's read of the whole prompt.
	ModelServerReadTimeoutMs = 300_000
	// ModelServerStallTimeoutMs: fifteen minutes without an event, three
	// times the read timeout, so a slow but healthy turn is not stopped
	// between two events.
	ModelServerStallTimeoutMs = 900_000
	// ModelServerTurnTimeoutMs: three hours for a whole run.
	ModelServerTurnTimeoutMs = 10_800_000
)

// MaxTimeoutMs is the largest value a read, turn or stall timeout accepts,
// agent-level or per profile: 30 days. A run is converted to a
// time.Duration in nanoseconds, which overflows above about 9.2e12 ms
// (106 days) and then turns negative: a stall check silently skipped, a
// read deadline already past.
const MaxTimeoutMs = 30 * 24 * 60 * 60 * 1000

// checkTimeoutMax refuses a numeric raw value above MaxTimeoutMs for the
// key where (compared as a float, so a huge YAML number is caught before
// any conversion to int).
func checkTimeoutMax(where string, raw any) error {
	if f, ok := toFloat(raw); ok && f > MaxTimeoutMs {
		return fmt.Errorf("config: %s must be at most %d milliseconds (30 days), got %v", where, MaxTimeoutMs, raw)
	}
	return nil
}

// checkAgentTimeoutsMax applies checkTimeoutMax to the agent-level
// timeouts in the agent: section.
func checkAgentTimeoutsMax(agent map[string]any) error {
	for _, key := range []string{"read_timeout_ms", "turn_timeout_ms", "stall_timeout_ms"} {
		if err := checkTimeoutMax("agent."+key, agent[key]); err != nil {
			return err
		}
	}
	return nil
}

// ModelServerEnvVar is the variable an agent CLI of backend reads for its
// model server: ANTHROPIC_BASE_URL for claude, OPENAI_BASE_URL for codex
// (its built-in provider), and "" for a backend whose server is set in its
// own config file (opencode, cursor) or that has none (custom).
func ModelServerEnvVar(backend string) string {
	switch backend {
	case BackendClaude:
		return "ANTHROPIC_BASE_URL"
	case BackendCodex:
		return "OPENAI_BASE_URL"
	}
	return ""
}

// ModelServerKey is the server a model_server URL names, for sharing a
// quota pool: the host, lower-cased, and the port, so a claude profile on
// http://127.0.0.1:<port> and a codex profile on http://127.0.0.1:<port>/v1
// share one. Every loopback host (127.0.0.0/8, ::1, localhost) is one
// machine, "loopback". "" for a URL without an explicit port.
func ModelServerKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Port() == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if isLoopbackHost(host) {
		host = "loopback"
	}
	return net.JoinHostPort(host, u.Port())
}

// AssignModelServerPools gives every profile with a model_server and no pool
// of its own the quota pool of its server (ModelServerKey), with PoolAuto
// set. Profiles on one server share the pool; its name is the
// model_server_name one of them sets, else "model-<profile>" after the first
// of them by name. The name carries no host or port. An explicit pool wins
// for its profile. Two profiles on one server with different
// model_server_name values, or one pool name (a model_server_name, a
// derived name or an explicit pool) on two servers or on a server and
// profiles without one, are an error.
// config.Load calls it after ApplyProfileModelServer on every profile; the
// dashboard's profile save and delete call it for the whole set, so a
// derived name can follow a rename.
func AssignModelServerPools(profiles map[string]AgentProfile) error {
	type server struct {
		profiles []string // in name order
		named    string
		namedBy  string
	}
	servers := map[string]*server{}
	var keys []string
	for _, name := range slices.Sorted(maps.Keys(profiles)) {
		p := profiles[name]
		if p.PoolAuto {
			p.Pool, p.PoolAuto = "", false
			profiles[name] = p
		}
		key := ModelServerKey(p.ModelServerURL)
		if key == "" {
			continue
		}
		s := servers[key]
		if s == nil {
			s = &server{}
			servers[key] = s
			keys = append(keys, key)
		}
		s.profiles = append(s.profiles, name)
		if given := p.ModelServerName; given != "" {
			if s.named != "" && s.named != given {
				return fmt.Errorf("config: agent.profiles.%s.model_server_name %q and agent.profiles.%s.model_server_name %q name the same model server (%s); give them one name",
					s.namedBy, s.named, name, given, key)
			}
			s.named, s.namedBy = given, name
		}
	}
	// A pool is one quota: every profile in it must reach the same model
	// server, or none. Explicit pools count too: a hosted profile's
	// `pool: local` and a server named local, or a hosted `pool: model-box`
	// and the pool derived for profile box, would otherwise share a quota
	// across unrelated servers.
	type poolUse struct{ server, by string }
	serverOf := map[string]poolUse{}
	clash := func(pool string, a poolUse, b poolUse) error {
		where := func(u poolUse) string {
			if u.server == "" {
				return "agent.profiles." + u.by + ", no model server"
			}
			return "agent.profiles." + u.by + ", model server " + u.server
		}
		return fmt.Errorf("config: the pool %q names two servers (%s; %s); one quota pool cannot span both: set model_server_name or pool on one of them",
			pool, where(a), where(b))
	}
	for _, name := range slices.Sorted(maps.Keys(profiles)) {
		p := profiles[name]
		if p.Pool == "" {
			continue
		}
		use := poolUse{server: ModelServerKey(p.ModelServerURL), by: name}
		other, taken := serverOf[p.Pool]
		if taken && other.server != use.server {
			return clash(p.Pool, other, use)
		}
		if !taken {
			serverOf[p.Pool] = use
		}
	}
	for _, key := range keys {
		s := servers[key]
		pool := s.named
		if pool == "" {
			pool = poolSlug("model-" + s.profiles[0])
		}
		use := poolUse{server: key, by: cmp.Or(s.namedBy, s.profiles[0])}
		if other, taken := serverOf[pool]; taken && other.server != key {
			return clash(pool, other, use)
		}
		serverOf[pool] = use
		for _, name := range s.profiles {
			p := profiles[name]
			if p.Pool == "" {
				p.Pool, p.PoolAuto = pool, true
				profiles[name] = p
			}
		}
	}
	return nil
}

// ModelServerInClearText reports whether rawURL is a plain http:// URL to a
// host other than this machine's loopback: what the agent sends the server
// (prompts, code, any API key it adds) crosses the network unencrypted.
// itervox doctor warns about it.
func ModelServerInClearText(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "http") || u.Hostname() == "" {
		return false
	}
	return !isLoopbackHost(strings.ToLower(u.Hostname()))
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// poolSlug maps s to a valid pool name: runs of anything but [a-z0-9]
// become one '-', trimmed at both ends.
func poolSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		return "local"
	}
	return out
}

// ApplyProfileModelServer resolves and checks profile's model_server and
// model_server_name, and drops a pool derived earlier (AssignModelServerPools
// derives it again over the whole set). name is the
// profile's name (for messages); defaultCommand is agent.command, which a
// profile without a command runs. config.Load calls it for every profile,
// and the dashboard's profile save calls it for the saved profile.
//
// The model_server is an http(s) URL with a host and an explicit port, or a
// whole $VAR naming one, read from the environment now; an unset variable is
// an error. The
// profile must run claude or codex. A command that assigns the same
// variable to another URL is a conflict.
func ApplyProfileModelServer(name string, profile *AgentProfile, defaultCommand string) error {
	raw := strings.TrimSpace(profile.ModelServer)
	profile.ModelServer = raw
	profile.ModelServerURL = ""
	if profile.PoolAuto {
		profile.Pool, profile.PoolAuto = "", false
	}
	profile.ModelServerName = strings.TrimSpace(profile.ModelServerName)
	if profile.ModelServerName != "" {
		if raw == "" {
			return fmt.Errorf("config: agent.profiles.%s.model_server_name is set without a model_server", name)
		}
		if !ValidPoolName(profile.ModelServerName) {
			return fmt.Errorf("config: agent.profiles.%s.model_server_name %q must match [a-z0-9-]+ (it names the quota pool)", name, profile.ModelServerName)
		}
	}
	if raw == "" {
		return nil
	}
	where := "config: agent.profiles." + name + ".model_server"
	resolved := raw
	if m := envVarRe.FindStringSubmatch(raw); m != nil {
		resolved = strings.TrimSpace(os.Getenv(m[1]))
		if resolved == "" {
			return fmt.Errorf("%s: %s is not set (set it in the environment or .itervox/.env)", where, raw)
		}
	} else if strings.Contains(raw, "$") {
		return fmt.Errorf("%s %q: a variable must be the whole value ($NAME)", where, raw)
	}
	if err := checkModelServerURL(resolved); err != nil {
		return fmt.Errorf("%s %q: %w", where, raw, err)
	}
	backend := ProfileRunsBackend(*profile, defaultCommand)
	envVar := ModelServerEnvVar(backend)
	if envVar == "" {
		if backend == "" {
			return fmt.Errorf("%s: cannot tell which backend the profile runs; set backend: claude or backend: codex on it", where)
		}
		return fmt.Errorf("%s: the %s backend reads its model server from its own config, not from a variable; model_server works with claude and codex profiles", where, backend)
	}
	command := profile.Command
	if strings.TrimSpace(command) == "" {
		command = defaultCommand
	}
	if assigned, ok := ModelServerURLs(command)[envVar]; ok && assigned != raw && assigned != resolved {
		return fmt.Errorf("%s %q conflicts with %s=%s in the profile's command; keep one of them", where, raw, envVar, assigned)
	}
	profile.ModelServerURL = resolved
	return nil
}

// CheckModelServerValue checks the shape of a model_server value without
// reading the environment: empty, a whole $VAR, or an http(s) URL with a
// host and an explicit port. The dashboard's profile save and quickstart
// call it before saving.
func CheckModelServerValue(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || envVarRe.MatchString(raw) {
		return nil
	}
	if strings.Contains(raw, "$") {
		return fmt.Errorf("a variable must be the whole value ($NAME)")
	}
	return checkModelServerURL(raw)
}

// checkModelServerURL accepts an http(s) URL with a host and an explicit
// port. The port is required, not taken from the scheme: local model
// servers listen on ports of their own, and a URL without one would
// silently mean 80 or 443, which is rarely where the server is.
func checkModelServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("must start with http:// or https://")
	}
	if u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("has no host")
	}
	if u.Port() == "" {
		return fmt.Errorf("has no port; write the port the server listens on, as in %s://%s:<port>", u.Scheme, u.Host)
	}
	if n, err := strconv.Atoi(u.Port()); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port %s is not between 1 and 65535", u.Port())
	}
	return nil
}

// ProfileModelServerEnv returns the NAME=value a run of profile on
// runBackend gets for its model_server, or "" when the profile sets none or
// the run moved to another backend than the profile's own (a per-issue or
// recovery switch): the URL serves the profile's backend's API. An empty
// runBackend is claude, the default runner.
func ProfileModelServerEnv(profile AgentProfile, runBackend, defaultCommand string) string {
	if profile.ModelServerURL == "" {
		return ""
	}
	if runBackend == "" {
		runBackend = BackendClaude
	}
	own := ProfileRunsBackend(profile, defaultCommand)
	if own != runBackend {
		return ""
	}
	envVar := ModelServerEnvVar(runBackend)
	if envVar == "" {
		return ""
	}
	return envVar + "=" + profile.ModelServerURL
}

// ProfileUsesModelServer reports whether a run of profile on runBackend
// talks to the profile's model_server: the profile sets one, and the run
// is on the profile's own backend (a per-issue or recovery switch to
// another backend leaves the server behind, as ProfileModelServerEnv
// does). An empty runBackend is claude, the default runner. A model-server
// variable written into the command (`ANTHROPIC_BASE_URL=… claude`) does
// not count: such a profile sets its timeouts itself.
func ProfileUsesModelServer(profile AgentProfile, runBackend, defaultCommand string) bool {
	if profile.ModelServerURL == "" && strings.TrimSpace(profile.ModelServer) == "" {
		return false
	}
	if runBackend == "" {
		runBackend = BackendClaude
	}
	return ProfileRunsBackend(profile, defaultCommand) == runBackend
}

// RunTimeouts are a run's three timeouts, in milliseconds (see
// AgentConfig.ReadTimeoutMs, TurnTimeoutMs and StallTimeoutMs).
type RunTimeouts struct {
	ReadMs  int
	TurnMs  int
	StallMs int
}

// ProfileTimeouts returns the timeouts for a run of profile on runBackend,
// given the agent-level ones. A positive value the profile sets wins.
// Otherwise a run that uses the profile's model_server
// (ProfileUsesModelServer: a profile with model_server, on its own
// backend) gets at least the ModelServer*TimeoutMs defaults, without
// lowering a larger agent-level value or enabling a turn or stall timeout
// the agent level turned off (0). Every other run uses the agent-level
// values.
func ProfileTimeouts(profile AgentProfile, runBackend, defaultCommand string, agent RunTimeouts) RunTimeouts {
	slow := ProfileUsesModelServer(profile, runBackend, defaultCommand)
	pick := func(own, agentValue, slowDefault int) int {
		if own > 0 {
			return own
		}
		if slow && agentValue > 0 {
			return max(agentValue, slowDefault)
		}
		return agentValue
	}
	return RunTimeouts{
		ReadMs:  pick(profile.ReadTimeoutMs, agent.ReadMs, ModelServerReadTimeoutMs),
		TurnMs:  pick(profile.TurnTimeoutMs, agent.TurnMs, ModelServerTurnTimeoutMs),
		StallMs: pick(profile.StallTimeoutMs, agent.StallMs, ModelServerStallTimeoutMs),
	}
}

// profileModelServerFields reads model_server, model_server_name and the
// three per-profile timeouts. A timeout is absent or a positive number of
// milliseconds up to MaxTimeoutMs: a profile cannot turn one off (the
// agent-level setting does that).
func profileModelServerFields(m map[string]any, name string) (modelServer, serverName string, t RunTimeouts, err error) {
	str := func(key string) (string, error) {
		raw, ok := m[key]
		if !ok || raw == nil {
			return "", nil
		}
		s, isStr := raw.(string)
		if !isStr {
			return "", fmt.Errorf("config: agent.profiles.%s.%s must be a string, got %T", name, key, raw)
		}
		return strings.TrimSpace(s), nil
	}
	if modelServer, err = str("model_server"); err != nil {
		return "", "", t, err
	}
	if serverName, err = str("model_server_name"); err != nil {
		return "", "", t, err
	}
	read := func(key string) (int, error) {
		raw, ok := m[key]
		if !ok || raw == nil {
			return 0, nil
		}
		if err := checkTimeoutMax("agent.profiles."+name+"."+key, raw); err != nil {
			return 0, err
		}
		n, isInt := toInt(raw)
		if !isInt || n <= 0 {
			return 0, fmt.Errorf("config: agent.profiles.%s.%s must be a positive number of milliseconds, got %v (a profile cannot turn a timeout off)", name, key, raw)
		}
		return n, nil
	}
	if t.ReadMs, err = read("read_timeout_ms"); err != nil {
		return "", "", t, err
	}
	if t.TurnMs, err = read("turn_timeout_ms"); err != nil {
		return "", "", t, err
	}
	if t.StallMs, err = read("stall_timeout_ms"); err != nil {
		return "", "", t, err
	}
	return modelServer, serverName, t, nil
}
