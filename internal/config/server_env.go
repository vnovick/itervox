package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Server bind overrides from the process environment (CORE-058), for
// containers (bind 0.0.0.0) and PaaS platforms that inject $PORT.
//
// Precedence, highest first:
//
//	host: ITERVOX_SERVER_HOST > server.host > 127.0.0.1
//	port: ITERVOX_SERVER_PORT > PORT > server.port > 8090
//
// There is no daemon CLI flag for either value. An explicit YAML `port: 0`
// (OS picks) still holds unless an env var overrides it; `0` in an env var
// means the same. Variables are read with os.LookupEnv, so a variable that is
// present but empty or malformed is a hard config error naming it — never
// silently ignored (the YAML port keeps its lenient parse).
//
// The environment is fixed for the life of the process: a WORKFLOW.md reload
// re-reads the same values, so changing the bind through the environment
// needs a restart.
const (
	EnvServerHost  = "ITERVOX_SERVER_HOST"
	EnvServerPort  = "ITERVOX_SERVER_PORT"
	EnvGenericPort = "PORT"
)

// applyServerEnv resolves cfg.Server.Host/Port against the environment and
// records which source won in HostSource/PortSource. yamlHost / yamlPort say
// whether WORKFLOW.md set a (valid) value.
func applyServerEnv(cfg *Config, yamlHost, yamlPort bool, lookup func(string) (string, bool)) error {
	cfg.Server.HostSource = "default"
	if yamlHost {
		cfg.Server.HostSource = "server.host"
	}
	if v, ok := lookup(EnvServerHost); ok {
		host, err := parseBindHost(v)
		if err != nil {
			return fmt.Errorf("config: %s=%q: %w", EnvServerHost, v, err)
		}
		cfg.Server.Host = host
		cfg.Server.HostSource = EnvServerHost
	}

	cfg.Server.PortSource = "default"
	if yamlPort {
		cfg.Server.PortSource = "server.port"
	}
	// Only the winning source is validated (M4-close): a platform-injected
	// $PORT that is junk must not break a daemon whose operator pinned
	// ITERVOX_SERVER_PORT. The first PRESENT variable wins; a present but
	// invalid winner is still a hard error.
	for _, key := range []string{EnvServerPort, EnvGenericPort} { // first present wins
		v, ok := lookup(key)
		if !ok {
			continue
		}
		port, err := parseBindPort(v)
		if err != nil {
			return fmt.Errorf("config: %s=%q: %w", key, v, err)
		}
		cfg.Server.Port = &port
		cfg.Server.PortSource = key
		break
	}
	return nil
}

// parseBindHost accepts an IP literal (IPv6 with or without brackets) or a
// host name. It rejects the shapes an operator is likely to paste by mistake:
// a URL, a host:port pair, a path, or anything with whitespace.
func parseBindHost(raw string) (string, error) {
	h := strings.TrimSpace(raw)
	if h == "" {
		return "", fmt.Errorf("empty host")
	}
	if strings.ContainsAny(h, " \t/\\@?#") || strings.Contains(h, "://") {
		return "", fmt.Errorf("want a bare host name or IP address (no scheme, path or spaces)")
	}
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	if strings.Contains(h, ":") && net.ParseIP(h) == nil {
		return "", fmt.Errorf("want a bare host (set the port with %s or %s, not host:port)", EnvServerPort, EnvGenericPort)
	}
	return h, nil
}

// parseBindPort accepts a decimal port 0..65535 (0 = OS picks).
func parseBindPort(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty port")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("port is not an integer")
	}
	if n < 0 || n > 65535 {
		return 0, fmt.Errorf("port out of range 0-65535")
	}
	return n, nil
}
