package procgroup

import (
	"errors"
	"testing"
)

// fakeShellHost is a Windows host for resolveShell: env vars, existing
// files and what PATH resolves bash to.
type fakeShellHost struct {
	env      map[string]string
	files    map[string]bool
	pathBash string
}

func (h fakeShellHost) lookup() shellLookup {
	return shellLookup{
		getenv: func(k string) string { return h.env[k] },
		isFile: func(p string) bool { return h.files[p] },
		lookPath: func(name string) (string, error) {
			if name == "bash" && h.pathBash != "" {
				return h.pathBash, nil
			}
			return "", errors.New("not found")
		},
	}
}

// TestWindowsShellResolution pins the Windows order: $ITERVOX_SHELL, then
// Git for Windows' bash under %ProgramFiles%, then bash on PATH (never the
// WSL launcher), then "" — direct exec of the agent CLI.
func TestWindowsShellResolution(t *testing.T) {
	const gitBash = `C:\Program Files\Git\bin\bash.exe`
	const pathBash = `D:\tools\msys64\usr\bin\bash.exe`
	full := fakeShellHost{
		env:      map[string]string{ShellEnv: `E:\custom\bash.exe`, "ProgramFiles": `C:\Program Files`, "SystemRoot": `C:\Windows`},
		files:    map[string]bool{gitBash: true},
		pathBash: pathBash,
	}
	cases := []struct {
		name string
		host func(fakeShellHost) fakeShellHost
		want string
	}{
		{"ITERVOX_SHELL wins over everything", func(h fakeShellHost) fakeShellHost { return h }, `E:\custom\bash.exe`},
		{"then Git Bash under ProgramFiles", func(h fakeShellHost) fakeShellHost {
			h.env = map[string]string{"ProgramFiles": `C:\Program Files`, "SystemRoot": `C:\Windows`}
			return h
		}, gitBash},
		{"ProgramFiles with a trailing slash", func(h fakeShellHost) fakeShellHost {
			h.env = map[string]string{"ProgramFiles": `C:\Program Files\`}
			return h
		}, gitBash},
		{"then bash on PATH when Git Bash is not installed", func(h fakeShellHost) fakeShellHost {
			h.env = map[string]string{"ProgramFiles": `C:\Program Files`, "SystemRoot": `C:\Windows`}
			h.files = nil
			return h
		}, pathBash},
		{"the WSL launcher on PATH does not count", func(h fakeShellHost) fakeShellHost {
			h.env = map[string]string{"ProgramFiles": `C:\Program Files`, "SystemRoot": `C:\Windows`}
			h.files = nil
			h.pathBash = `C:\WINDOWS\system32\bash.exe`
			return h
		}, ""},
		{"nothing found: direct exec", func(h fakeShellHost) fakeShellHost {
			return fakeShellHost{}
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveShell("windows", "bash", tc.host(full).lookup()); got != tc.want {
				t.Fatalf("resolveShell = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestShellUnixUnchanged: off Windows the caller's shell is returned as is,
// whatever ITERVOX_SHELL and PATH say.
func TestShellUnixUnchanged(t *testing.T) {
	h := fakeShellHost{env: map[string]string{ShellEnv: "/opt/other/bash"}, pathBash: "/usr/bin/bash"}
	for _, goos := range []string{"linux", "darwin"} {
		if got := resolveShell(goos, "/bin/zsh", h.lookup()); got != "/bin/zsh" {
			t.Fatalf("%s: resolveShell = %q, want /bin/zsh", goos, got)
		}
	}
}

// TestShellPathListMSYS: on Windows a PATH list is rewritten for Git Bash;
// elsewhere it is untouched.
func TestShellPathListMSYS(t *testing.T) {
	in := `C:\Users\me\.itervox\shims;d:\bin\;\\server\share`
	if got, want := shellPathList("windows", in), `/c/Users/me/.itervox/shims:/d/bin://server/share`; got != want {
		t.Fatalf("windows: %q, want %q", got, want)
	}
	if got := shellPathList("linux", "/a/b:/c"); got != "/a/b:/c" {
		t.Fatalf("linux: %q", got)
	}
}
