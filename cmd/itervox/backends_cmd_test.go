package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// TestUnprobedCredentialChecksNameNewBackends: doctor --deploy lists, as
// skipped with a how-to, the login of every OpenCode, Cursor or custom
// backend in use, and nothing for one not in use.
func TestUnprobedCredentialChecksNameNewBackends(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Command = "claude"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"oc":   {Command: "opencode"},
		"cur":  {Command: "/opt/bin/cursor-agent"},
		"mine": {Command: "./wrap.sh", Backend: "custom"},
	}
	checks := unprobedCredentialChecks(backendsInUse(cfg))
	var names []string
	for _, c := range checks {
		names = append(names, c.Name)
		if c.Status != deploySkipped || c.Detail == "" {
			t.Errorf("%s: want a skipped check with a how-to, got %+v", c.Name, c)
		}
	}
	if want := []string{"opencode credentials", "cursor credentials", "custom credentials"}; !slices.Equal(names, want) {
		t.Fatalf("checks = %v, want %v", names, want)
	}
	cfg.Agent.Profiles = nil
	if got := unprobedCredentialChecks(backendsInUse(cfg)); len(got) != 0 {
		t.Errorf("no new backend in use, got %+v", got)
	}
}

// TestDiscoverModelsOpenCode: opencode is a refreshable backend; "all"
// includes it only when `opencode models` found some, so a refresh never
// blanks a hand-written list.
func TestDiscoverModelsOpenCode(t *testing.T) {
	if !IsAcceptedModelBackend("opencode") || IsAcceptedModelBackend("cursor") {
		t.Fatal("opencode is refreshable, cursor is not")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	orig := listOpenCodeModels
	t.Cleanup(func() { listOpenCodeModels = orig })

	listOpenCodeModels = func() []agent.ModelOption { return nil }
	if _, ok := discoverModels("all")["opencode"]; ok {
		t.Error(`"all" must leave opencode out when none were found`)
	}
	if list, ok := discoverModels("opencode")["opencode"]; !ok || len(list) != 0 {
		t.Errorf(`"opencode" reports its (empty) list, got %v %v`, list, ok)
	}
	listOpenCodeModels = func() []agent.ModelOption { return []agent.ModelOption{{ID: "a/b", Label: "a/b"}} }
	got := discoverModels("all")
	if len(got["opencode"]) != 1 || len(got["claude"]) == 0 || len(got["codex"]) == 0 {
		t.Errorf(`"all" = %v`, got)
	}
	if only := discoverModels("codex"); len(only) != 1 {
		t.Errorf(`"codex" discovers only codex, got %v`, only)
	}
}

// TestValidateBackendNewBackends: startup validates an OpenCode, Cursor or
// custom CLI once (custom once per command); a failing default CLI is
// fatal, a failing profile CLI only warns.
func TestValidateBackendNewBackends(t *testing.T) {
	orig := validateExtraBackendCLI
	t.Cleanup(func() { validateExtraBackendCLI = orig })
	var calls []string
	failing := ""
	validateExtraBackendCLI = func(backend, command string) error {
		calls = append(calls, backend+"|"+command)
		if backend == failing {
			return errors.New("not found")
		}
		return nil
	}
	cfg := &config.Config{}
	cfg.Agent.Command = "opencode"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"a": {Command: "./one.sh", Backend: "custom"},
		"b": {Command: "./two.sh", Backend: "custom"},
		"c": {Command: "cursor-agent"},
	}
	seen := map[string]struct{}{}
	if err := validateBackend("opencode", "", seen, cfg); err != nil {
		t.Fatal(err)
	}
	_ = validateBackend("opencode", "", seen, cfg) // deduplicated
	for _, name := range []string{"a", "b", "c"} {
		p := cfg.Agent.Profiles[name]
		_ = validateBackend(configuredBackend(p.Command, p.Backend), name, seen, cfg)
	}
	want := []string{"opencode|opencode", "custom|./one.sh", "custom|./two.sh", "cursor|cursor-agent"}
	if !slices.Equal(calls, want) {
		t.Fatalf("validations = %v, want %v", calls, want)
	}

	failing = "cursor"
	if err := validateBackend("cursor", "", map[string]struct{}{}, cfg); err == nil || !strings.Contains(err.Error(), "cursor CLI not found") {
		t.Errorf("a failing default CLI must be fatal, got %v", err)
	}
	if err := validateBackend("cursor", "c", map[string]struct{}{}, cfg); err != nil {
		t.Errorf("a failing profile CLI only warns, got %v", err)
	}
}

// TestAgentCLINamesIncludeNewCLIs: doctor's "agent CLI on PATH" check
// covers opencode and cursor-agent commands.
func TestAgentCLINamesIncludeNewCLIs(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Command = "opencode --model a/b"
	cfg.Agent.Profiles = map[string]config.AgentProfile{"c": {Command: "FOO=1 cursor-agent -p"}}
	if got := strings.Join(agentCLINames(cfg), ","); got != "cursor-agent,opencode" {
		t.Fatalf("agentCLINames = %s", got)
	}
}
