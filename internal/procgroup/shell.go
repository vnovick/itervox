package procgroup

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ShellEnv is the environment variable that names the bash executable the
// daemon uses on Windows for the command lines it runs through a login
// shell (agent CLIs given by name, workspace hooks). It is consulted on
// Windows only; Unix keeps $SHELL / bash.
const ShellEnv = "ITERVOX_SHELL"

// Shell returns the shell for a "-lc" command line on this platform. On
// Unix it is unixShell unchanged (callers pass $SHELL or "bash"). On
// Windows it is the first of: $ITERVOX_SHELL, Git for Windows' bash
// (%ProgramFiles%\Git\bin\bash.exe), then bash on PATH — skipping the WSL
// launcher in System32, whose bash runs inside a Linux VM that cannot see
// the agent CLI or Windows paths. "" means no bash was found: agent CLIs
// are then executed directly and hooks fail naming what is missing.
func Shell(unixShell string) string {
	return resolveShell(runtime.GOOS, unixShell, systemShellLookup)
}

// shellLookup is what resolveShell consults; tests inject it.
type shellLookup struct {
	getenv   func(string) string
	isFile   func(string) bool
	lookPath func(string) (string, error)
}

var systemShellLookup = shellLookup{
	getenv: os.Getenv,
	isFile: func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	},
	lookPath: exec.LookPath,
}

func resolveShell(goos, unixShell string, l shellLookup) string {
	if goos != "windows" {
		return unixShell
	}
	if s := l.getenv(ShellEnv); s != "" {
		return s
	}
	if pf := l.getenv("ProgramFiles"); pf != "" {
		if p := strings.TrimRight(pf, `\/`) + `\Git\bin\bash.exe`; l.isFile(p) {
			return p
		}
	}
	if p, err := l.lookPath("bash"); err == nil && !isWSLLauncher(p, l.getenv("SystemRoot")) {
		return p
	}
	return ""
}

// isWSLLauncher reports whether p is %SystemRoot%\System32\bash.exe, the
// WSL entry point. The comparison is on Windows path rules (either slash,
// any case) whatever the host, so it can be tested on Linux.
func isWSLLauncher(p, systemRoot string) bool {
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "/", `\`)) }
	dir := norm(p)
	if i := strings.LastIndexByte(dir, '\\'); i >= 0 {
		dir = dir[:i]
	}
	return dir == norm(strings.TrimRight(systemRoot, `\/`))+`\system32`
}

// ShellPathList converts a PATH-style list for use inside a command line
// run by Shell's bash. On Unix it is list unchanged. On Windows, Git Bash
// expects MSYS paths separated by ':' — `C:\a\b;D:\c` becomes
// `/c/a/b:/d/c` — because a Windows drive colon would split the entry.
func ShellPathList(list string) string {
	return shellPathList(runtime.GOOS, list)
}

func shellPathList(goos, list string) string {
	if goos != "windows" || list == "" {
		return list
	}
	parts := strings.Split(list, ";")
	for i, p := range parts {
		parts[i] = msysPath(p)
	}
	return strings.Join(parts, ":")
}

// msysPath converts one Windows path to its MSYS (Git Bash) form.
func msysPath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		rest := p[2:]
		if !strings.HasPrefix(rest, "/") {
			rest = "/" + rest
		}
		return "/" + strings.ToLower(p[:1]) + strings.TrimSuffix(rest, "/")
	}
	return p
}

func isASCIILetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
