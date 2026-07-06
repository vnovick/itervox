package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHookThreadsItervoxBinIntoSubprocess(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "captured.txt")
	t.Setenv("ITERVOX_BIN", "/path/to/dev-build/itervox")

	script := "printenv ITERVOX_BIN > " + outPath
	if err := RunHook(context.Background(), script, tmp, 5000); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read %s: %v", outPath, err)
	}
	got := strings.TrimSpace(string(data))
	if got != "/path/to/dev-build/itervox" {
		t.Errorf("hook saw ITERVOX_BIN=%q, want /path/to/dev-build/itervox", got)
	}
}

func TestHookEnvOverridesInheritedItervoxBin(t *testing.T) {
	t.Setenv("ITERVOX_BIN", "/correct/value")
	base := []string{
		"PATH=/usr/bin",
		"ITERVOX_BIN=/stale/inherited/value",
		"HOME=/tmp",
	}
	env := hookEnv(base)
	var found string
	for _, kv := range env {
		if strings.HasPrefix(kv, "ITERVOX_BIN=") {
			found = kv
		}
	}
	if found != "ITERVOX_BIN=/correct/value" {
		t.Errorf("expected daemon ITERVOX_BIN to override inherited; got %q", found)
	}
}

func TestHookEnvOmitsItervoxBinWhenUnset(t *testing.T) {
	t.Setenv("ITERVOX_BIN", "")
	base := []string{"PATH=/usr/bin", "HOME=/tmp"}
	env := hookEnv(base)
	for _, kv := range env {
		if strings.HasPrefix(kv, "ITERVOX_BIN=") {
			t.Errorf("ITERVOX_BIN should not be set; got %q", kv)
		}
	}
}
