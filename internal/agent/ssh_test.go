package agent

import (
	"slices"
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
