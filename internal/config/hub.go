package config

import (
	"fmt"
	"net"
	"strings"
)

// Hub defaults (#228).
const (
	// DefaultHubPort is hub.port when unset; when it is busy the hub takes
	// the next free port and records it in the hub lock.
	DefaultHubPort = 8089
	// DefaultHubBind is hub.bind when unset: loopback only.
	DefaultHubBind = "127.0.0.1"
)

// HubConfig is the `hub` section (#228): whether this daemon takes part in
// the machine's itervox hub, and where the hub listens when this daemon is
// the one serving it. Machine-wide in practice, so it belongs in the user
// settings layer (~/.itervox/settings.jsonc); the daemon that wins the hub
// election uses its own values. Read at startup only (a reload does not
// move the hub).
type HubConfig struct {
	// Enabled (hub.enabled, default true) lets this daemon become the hub
	// or join it as a member. False is the same as --no-hub.
	Enabled bool
	// Port (hub.port, default 8089) is the hub's port; 0 lets the OS pick.
	Port int
	// Bind (hub.bind, default 127.0.0.1) is the hub's listen address.
	// Every hub route except the health probe needs a hub token, so
	// exposing it (0.0.0.0, a Tailscale address) is safe with tokens only.
	Bind string
}

// parseHubConfig reads the hub section.
func parseHubConfig(m map[string]any) (HubConfig, error) {
	h := HubConfig{
		Enabled: boolField(m, "enabled", true),
		Port:    intField(m, "port", DefaultHubPort),
		Bind:    strings.TrimSpace(strField(m, "bind", DefaultHubBind)),
	}
	if h.Port < 0 || h.Port > 65535 {
		return h, fmt.Errorf("config: hub.port must be between 0 and 65535 (got %d)", h.Port)
	}
	if h.Bind != "localhost" && net.ParseIP(h.Bind) == nil {
		return h, fmt.Errorf("config: hub.bind must be an IP address or localhost (got %q)", h.Bind)
	}
	return h, nil
}
