package guardrails

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWindowsCmdShimWrapper: on Windows each sh shim gets a name.cmd
// wrapper next to it that forwards to the same entrypoint, policy and tool
// with every argument and the exit status, and carries shimMarker so
// resolveReal never picks it as the real program. No wrapper is written
// elsewhere.
func TestWindowsCmdShimWrapper(t *testing.T) {
	dir := t.TempDir()
	exe := `C:\Program Files\itervox\itervox.exe`
	policy := `C:\Users\me\AppData\Local\Temp\itervox-guard-100%\policy.json`
	if err := writeNativeShim("windows", dir, "git", exe, policy); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "git.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	want := "@echo off\r\nrem " + shimMarker + "\r\n" +
		`"C:\Program Files\itervox\itervox.exe" guardrail-shim "C:\Users\me\AppData\Local\Temp\itervox-guard-100%%\policy.json" "git" %*` + "\r\n" +
		"exit /b %ERRORLEVEL%\r\n"
	if got != want {
		t.Fatalf("git.cmd =\n%q\nwant\n%q", got, want)
	}
	if !isShim(filepath.Join(dir, "git.cmd")) {
		t.Fatal("the .cmd wrapper must carry shimMarker")
	}

	other := t.TempDir()
	for _, goos := range []string{"linux", "darwin"} {
		if err := writeNativeShim(goos, other, "git", "/usr/local/bin/itervox", "/tmp/p.json"); err != nil {
			t.Fatal(err)
		}
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatalf("writeNativeShim wrote %d file(s) off Windows", len(entries))
	}
}
