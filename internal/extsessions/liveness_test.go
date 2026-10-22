package extsessions

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// #178 step 3: a hand-started session is shown as running while the
// original process may still be running. A process tied to the transcript (open file, its
// session ID in the arguments) is certain; one in the session's directory,
// one whose directory is unreadable, an unreadable process list and a
// recent write are precautions; only with none of them is the session
// treated as exited.
func TestCheckLivenessRefusesWhileTheProcessRuns(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	idle := Session{ID: "sess-1234-abcd", Backend: BackendClaude, Cwd: "/work/app", Path: path, UpdatedAt: now.Add(-time.Hour)}
	claude := func(pid int, cwd string, args ...string) Process {
		return Process{PID: pid, Name: "claude", Args: append([]string{"claude"}, args...), Cwd: cwd}
	}
	cases := []struct {
		name    string
		s       Session
		procs   []Process
		listErr error
		running bool
		certain bool
		reason  string
	}{
		{"open transcript", idle, []Process{{PID: 7, Name: "node", Args: []string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js"}, Cwd: "/elsewhere", Files: []string{path}}}, nil, true, true, "has the transcript open"},
		{"resumed by id", idle, []Process{claude(8, "/elsewhere", "--resume", "sess-1234-abcd")}, nil, true, true, "started for this session"},
		{"same directory", idle, []Process{claude(9, "/work/app")}, nil, true, false, "in the session's directory"},
		{"unreadable directory", idle, []Process{claude(10, "")}, nil, true, false, "could not be read"},
		{"process list unreadable", idle, nil, errors.New("no /proc"), true, false, "could not inspect running processes"},
		{"recent write", Session{ID: idle.ID, Backend: BackendClaude, Cwd: "/work/app", Path: path, UpdatedAt: now.Add(-30 * time.Second)}, nil, nil, true, false, "written to"},
		{"exited", idle, []Process{claude(11, "/other"), {PID: 12, Name: "codex", Args: []string{"codex"}, Cwd: "/work/app"}, {PID: 13, Name: "vim", Args: []string{"vim"}, Cwd: "/work/app"}}, nil, false, true, "no claude process"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := CheckLiveness(c.s, c.procs, c.listErr, now)
			if l.Running != c.running || l.Certain != c.certain || !strings.Contains(l.Reason, c.reason) {
				t.Fatalf("liveness = %+v, want running=%v certain=%v reason~%q", l, c.running, c.certain, c.reason)
			}
		})
	}
}

func TestProcessBackendRecognisesTheCLIs(t *testing.T) {
	for _, c := range []struct {
		p    Process
		want string
	}{
		{Process{Name: "claude", Args: []string{"/opt/claude/bin/claude", "--resume", "x"}}, BackendClaude},
		{Process{Name: "node", Args: []string{"node", "--no-warnings", "/x/@anthropic-ai/claude-code/cli.js"}}, BackendClaude},
		{Process{Name: "codex", Args: []string{"/usr/local/bin/codex", "exec"}}, BackendCodex},
		{Process{Name: "node", Args: []string{"node", "/x/node_modules/@openai/codex/bin/codex.js"}}, BackendCodex},
		{Process{Name: "vim", Args: []string{"vim", "claude.md"}}, ""},
		{Process{Name: "zsh", Args: []string{"zsh"}}, ""},
	} {
		if got := ProcessBackend(c.p); got != c.want {
			t.Fatalf("ProcessBackend(%v) = %q, want %q", c.p.Args, got, c.want)
		}
	}
}

// The Linux inspector reads /proc read-only: cmdline, comm, the cwd link
// and the fd links of agent processes only.
func TestProcProcessesReadsCwdAndOpenFiles(t *testing.T) {
	root := t.TempDir()
	mk := func(pid int, comm string, args []string, cwd string, fds map[string]string) {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(args, "\x00")+"\x00"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "stat"), []byte(strconv.Itoa(pid)+" ("+comm+" (x)) S 102 1 1 0"), 0o644)
		_ = os.Symlink(cwd, filepath.Join(dir, "cwd"))
		for fd, target := range fds {
			_ = os.Symlink(target, filepath.Join(dir, "fd", fd))
		}
	}
	mk(101, "claude", []string{"claude", "-p"}, "/work/app", map[string]string{"1": "pipe:[1]", "5": "/home/u/.claude/projects/-work-app/s.jsonl"})
	mk(102, "bash", []string{"bash"}, "/work/app", map[string]string{"5": "/secret/file"})
	_ = os.MkdirAll(filepath.Join(root, "self"), 0o755)
	procs, err := procProcesses(root)
	if err != nil {
		t.Fatal(err)
	}
	byPID := map[int]Process{}
	for _, p := range procs {
		byPID[p.PID] = p
	}
	c := byPID[101]
	if c.PPID != 102 {
		t.Fatalf("PPID = %d, want 102 (read from stat after the command name)", c.PPID)
	}
	if c.Name != "claude" || c.Cwd != "/work/app" || len(c.Files) != 1 || c.Files[0] != "/home/u/.claude/projects/-work-app/s.jsonl" {
		t.Fatalf("claude process = %+v", c)
	}
	if b := byPID[102]; b.Cwd != "" || b.Files != nil {
		t.Fatalf("a non-agent process had its cwd or files read: %+v", b)
	}
}

func TestParsePSAndLsof(t *testing.T) {
	procs := parsePS([]byte("  1     0 3-01:02:03 /sbin/launchd\n 42  7    01:05 /usr/local/bin/claude --resume abc\n 99 1 00:00 ps -x\n 50 1 x:y node\nbogus\n"), 99)
	if len(procs) != 3 || procs[1].PID != 42 || procs[1].PPID != 7 || procs[1].Name != "claude" || procs[1].Args[2] != "abc" {
		t.Fatalf("parsePS = %+v", procs)
	}
	if procs[0].elapsed != 73*time.Hour+2*time.Minute+3*time.Second || procs[1].elapsed != 65*time.Second || procs[2].elapsed != -1 {
		t.Fatalf("elapsed = %v %v %v", procs[0].elapsed, procs[1].elapsed, procs[2].elapsed)
	}
	cwd, files := parseLsof([]byte("p42\nfcwd\ntDIR\nn/work/app\nftxt\ntREG\nn/usr/local/bin/claude\nf7w\ntREG\nn/Users/u/.claude/projects/x/s.jsonl\nf8u\ntIPv4\nnlocalhost:1\n"))
	if cwd != "/work/app" || len(files) != 2 || files[1] != "/Users/u/.claude/projects/x/s.jsonl" {
		t.Fatalf("parseLsof = %q %v", cwd, files)
	}
}

// Scanner.Scan attaches liveness to every discovered session.
func TestScannerAttachesLiveness(t *testing.T) {
	now := time.Now()
	claude, _ := fixtures(t, now)
	sc := Scanner{
		Options: Options{ClaudeProjectsDir: claude, Window: 24 * time.Hour, Roots: []string{"/work/app"}},
		Processes: func() ([]Process, error) {
			return []Process{{PID: 5, Name: "claude", Args: []string{"claude"}, Cwd: "/work/app/web"}}, nil
		},
	}
	got, procs, err := sc.Scan(now)
	if err != nil {
		t.Fatal(err)
	}
	if web := find(t, got, idClaudeWeb); !web.Running || !strings.Contains(web.RunningReason, "directory") {
		t.Fatalf("web session = %+v", web)
	}
	if app := find(t, got, idClaudeApp); app.Running {
		t.Fatalf("app session = %+v", app)
	}
	if web := find(t, got, idClaudeWeb); web.PID != 5 || len(procs) != 1 {
		t.Fatalf("web session PID = %d, procs = %v: the process it rests on and the list are returned", web.PID, procs)
	}
}

// The parent chain of a process, nearest first, stops at PID 1, an unknown
// parent or a loop.
func TestAncestorsWalksTheParentChain(t *testing.T) {
	procs := []Process{{PID: 50, PPID: 40}, {PID: 40, PPID: 30}, {PID: 30, PPID: 1}, {PID: 7, PPID: 8}, {PID: 8, PPID: 7}}
	if got := Ancestors(procs, 50); len(got) != 2 || got[0] != 40 || got[1] != 30 {
		t.Fatalf("Ancestors(50) = %v", got)
	}
	if got := Ancestors(procs, 7); len(got) != 1 || got[0] != 8 {
		t.Fatalf("Ancestors(7) = %v (a loop must stop)", got)
	}
	if got := Ancestors(procs, 99); len(got) != 0 {
		t.Fatalf("Ancestors(unknown) = %v", got)
	}
}
