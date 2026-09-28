package agent

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func TestSSHStrictHostDefault(t *testing.T) {
	// Reset to known state at the end so other tests in this package see the
	// production default ("accept-new") regardless of test ordering.
	t.Cleanup(func() {
		SetSSHStrictHostDefault("accept-new")
		SetSSHStrictHostOverrides(nil)
	})

	if got := sshStrictHostMode("any-host"); got != "accept-new" {
		t.Errorf("default mode = %q, want %q (TOFU)", got, "accept-new")
	}
}

func TestSSHStrictHostOverrides(t *testing.T) {
	t.Cleanup(func() {
		SetSSHStrictHostDefault("accept-new")
		SetSSHStrictHostOverrides(nil)
	})

	SetSSHStrictHostOverrides(map[string]string{
		"prod.example.com":    "yes",
		"sandbox.example.com": "no",
		"bad.example.com":     "garbage", // should be filtered out
	})

	if got := sshStrictHostMode("prod.example.com"); got != "yes" {
		t.Errorf("prod override = %q, want %q", got, "yes")
	}
	if got := sshStrictHostMode("sandbox.example.com"); got != "no" {
		t.Errorf("sandbox override = %q, want %q", got, "no")
	}
	// Unconfigured host falls back to default.
	if got := sshStrictHostMode("other.example.com"); got != "accept-new" {
		t.Errorf("fallback = %q, want %q", got, "accept-new")
	}
	// Invalid mode in input is filtered — host falls through to default.
	if got := sshStrictHostMode("bad.example.com"); got != "accept-new" {
		t.Errorf("invalid-mode host = %q, want %q (filtered)", got, "accept-new")
	}
}

func TestSSHStrictHostOption(t *testing.T) {
	t.Cleanup(func() {
		SetSSHStrictHostDefault("accept-new")
		SetSSHStrictHostOverrides(nil)
	})

	got := sshStrictHostOption("any-host")
	want := []string{"-o", "StrictHostKeyChecking=accept-new"}
	if !slices.Equal(got, want) {
		t.Errorf("option = %v, want %v", got, want)
	}

	SetSSHStrictHostDefault("yes")
	got = sshStrictHostOption("any-host")
	want = []string{"-o", "StrictHostKeyChecking=yes"}
	if !slices.Equal(got, want) {
		t.Errorf("option after default change = %v, want %v", got, want)
	}
}

func TestSSHStrictHostDefaultRejectsInvalidMode(t *testing.T) {
	t.Cleanup(func() {
		SetSSHStrictHostDefault("accept-new")
	})

	SetSSHStrictHostDefault("garbage-mode") // should be ignored
	if got := sshStrictHostMode("any-host"); got != "accept-new" {
		t.Errorf("default after invalid SetSSHStrictHostDefault = %q, want %q (ignored)", got, "accept-new")
	}
}

// TestSSHStrictHostInvalidModeLogsWarning is CORE-140's runtime half: an
// invalid mode reaching the setters (config validation is the primary gate)
// must not be dropped silently — each rejected value logs a warning naming
// it, and the effective mode is left unchanged.
func TestSSHStrictHostInvalidModeLogsWarning(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		SetSSHStrictHostDefault("accept-new")
		SetSSHStrictHostOverrides(nil)
	})

	SetSSHStrictHostDefault("strict")
	SetSSHStrictHostOverrides(map[string]string{"prod.example.com": "Yes", "ok.example.com": "yes"})

	out := buf.String()
	if !strings.Contains(out, `mode=strict`) {
		t.Errorf("invalid default mode must log a warning naming it; log output:\n%s", out)
	}
	if !strings.Contains(out, `host=prod.example.com`) || !strings.Contains(out, `mode=Yes`) {
		t.Errorf("invalid per-host mode must log a warning naming host and mode; log output:\n%s", out)
	}
	if strings.Contains(out, "ok.example.com") {
		t.Errorf("a valid per-host mode must not warn; log output:\n%s", out)
	}
	if got := sshStrictHostMode("any-host"); got != "accept-new" {
		t.Errorf("default after invalid mode = %q, want accept-new (unchanged)", got)
	}
	if got := sshStrictHostMode("ok.example.com"); got != "yes" {
		t.Errorf("valid override = %q, want yes", got)
	}
}
