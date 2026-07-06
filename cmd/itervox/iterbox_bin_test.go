package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRefreshItervoxBinSymlinkCreatesLink(t *testing.T) {
	tmp := t.TempDir()
	if err := refreshItervoxBinSymlink(tmp); err != nil {
		t.Fatalf("refreshItervoxBinSymlink: %v", err)
	}
	link := filepath.Join(tmp, ".itervox", "bin", "itervox")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink %s: %v", link, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if target != exe {
		t.Errorf("symlink target = %q, want os.Executable() %q", target, exe)
	}
}

func TestRefreshItervoxBinSymlinkRefreshesStaleLink(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, ".itervox", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(binDir, "itervox")
	if err := os.Symlink("/totally/stale/path", stale); err != nil {
		t.Fatal(err)
	}
	if err := refreshItervoxBinSymlink(tmp); err != nil {
		t.Fatalf("refreshItervoxBinSymlink: %v", err)
	}
	target, err := os.Readlink(stale)
	if err != nil {
		t.Fatal(err)
	}
	if target == "/totally/stale/path" {
		t.Errorf("expected symlink to be refreshed; still points at stale %q", target)
	}
}

func TestSetItervoxBinEnvIsIdempotent(t *testing.T) {
	t.Setenv("ITERVOX_BIN", "/external/pinned/value")
	setItervoxBinEnv()
	if got := os.Getenv("ITERVOX_BIN"); got != "/external/pinned/value" {
		t.Errorf("ITERVOX_BIN = %q; external pin must win", got)
	}
}
