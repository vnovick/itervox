package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// A profile's model_server (agent.profiles.<name>.model_server): the base
// URL of a model server the profile's agent CLI talks to instead of its
// vendor's, without writing `ANTHROPIC_BASE_URL=…` into the command.
// Itervox sets the variable the profile's backend reads when it runs the
// agent (ModelServerEnvVar), gives the profile its own quota pool unless it
// sets one (ModelServerPool), and lengthens its timeouts (ProfileTimeouts).

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

// ModelServerPool is the quota pool a profile on rawURL gets when it sets no
// pool: "local-<port>" for a loopback host (127.0.0.0/8, ::1, localhost),
// else "model-<host>-<port>", lower-case letters, digits and '-'. Profiles
// on the same server share it; a hosted backend's limit never holds it.
func ModelServerPool(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "local"
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	if isLoopbackHost(host) {
		return "local-" + port
	}
	return poolSlug("model-" + host + "-" + port)
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

// ApplyProfileModelServer resolves and checks profile's model_server, and
// sets its pool from it unless the profile sets pool itself. name is the
// profile's name (for messages); defaultCommand is agent.command, which a
// profile without a command runs. config.Load calls it for every profile,
// and the dashboard's profile save calls it for the saved profile.
//
// The model_server is an http(s) URL with a host, or a whole $VAR naming
// one, read from the environment now; an unset variable is an error. The
// profile must run claude or codex. A command that assigns the same
// variable to another URL is a conflict.
func ApplyProfileModelServer(name string, profile *AgentProfile, defaultCommand string) error {
	raw := strings.TrimSpace(profile.ModelServer)
	profile.ModelServer = raw
	profile.ModelServerURL = ""
	if profile.PoolAuto {
		profile.Pool, profile.PoolAuto = "", false
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
	if profile.Pool == "" {
		profile.Pool, profile.PoolAuto = ModelServerPool(resolved), true
	}
	return nil
}

// CheckModelServerValue checks the shape of a model_server value without
// reading the environment: empty, a whole $VAR, or an http(s) URL with a
// host. The dashboard's profile save calls it before saving.
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

// checkModelServerURL accepts an http(s) URL with a host.
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

// ProfileUsesModelServer reports whether runs of profile talk to a model
// server: a model_server, or a model-server variable assigned in its
// command (or, without one, in defaultCommand).
func ProfileUsesModelServer(profile AgentProfile, defaultCommand string) bool {
	if profile.ModelServerURL != "" || strings.TrimSpace(profile.ModelServer) != "" {
		return true
	}
	command := profile.Command
	if strings.TrimSpace(command) == "" {
		command = defaultCommand
	}
	return len(ModelServerURLs(command)) > 0
}

// RunTimeouts are a run's three timeouts, in milliseconds (see
// AgentConfig.ReadTimeoutMs, TurnTimeoutMs and StallTimeoutMs).
type RunTimeouts struct {
	ReadMs  int
	TurnMs  int
	StallMs int
}

// ProfileTimeouts returns the timeouts for a run of profile, given the
// agent-level ones. A positive value the profile sets wins. Otherwise a
// profile that uses a model server (ProfileUsesModelServer) gets at least
// the ModelServer*TimeoutMs defaults, without lowering a larger agent-level
// value or enabling a turn or stall timeout the agent level turned off (0).
// Every other profile runs with the agent-level values.
func ProfileTimeouts(profile AgentProfile, defaultCommand string, agent RunTimeouts) RunTimeouts {
	slow := ProfileUsesModelServer(profile, defaultCommand)
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

// profileModelServerFields reads model_server and the three per-profile
// timeouts. A timeout is absent or a positive number of milliseconds: a
// profile cannot turn one off (the agent-level setting does that).
func profileModelServerFields(m map[string]any, name string) (modelServer string, t RunTimeouts, err error) {
	if raw, ok := m["model_server"]; ok && raw != nil {
		s, isStr := raw.(string)
		if !isStr {
			return "", t, fmt.Errorf("config: agent.profiles.%s.model_server must be a string, got %T", name, raw)
		}
		modelServer = strings.TrimSpace(s)
	}
	read := func(key string) (int, error) {
		raw, ok := m[key]
		if !ok || raw == nil {
			return 0, nil
		}
		n, isInt := toInt(raw)
		if !isInt || n <= 0 {
			return 0, fmt.Errorf("config: agent.profiles.%s.%s must be a positive number of milliseconds, got %v (a profile cannot turn a timeout off)", name, key, raw)
		}
		return n, nil
	}
	if t.ReadMs, err = read("read_timeout_ms"); err != nil {
		return "", t, err
	}
	if t.TurnMs, err = read("turn_timeout_ms"); err != nil {
		return "", t, err
	}
	if t.StallMs, err = read("stall_timeout_ms"); err != nil {
		return "", t, err
	}
	return modelServer, t, nil
}
