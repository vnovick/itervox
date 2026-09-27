package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// CORE-063 — `itervox doctor --deploy`. The real subcommand runs in a
// re-executed test binary (daemon_child_test.go's hook) with fake gh, git,
// claude and codex on PATH, a fake Linear endpoint and a fake daemon /ready,
// all local: no network, no real CLI, no credentials.

const fakeGHToken = "gho_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789ab"

type doctorFixture struct {
	dir      string
	workflow string
	bin      string
	gitCalls string
	env      []string
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newDoctorFixture builds a project whose default agent is claude (API key in
// env → ok) with a codex profile (codex says "Not logged in" → fail).
func newDoctorFixture(t *testing.T, linearURL, sshHosts string) *doctorFixture {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCalls := filepath.Join(dir, "git-calls")
	writeExec(t, filepath.Join(bin, "claude"), "echo 'stub claude 0.0.0'\n")
	writeExec(t, filepath.Join(bin, "codex"), `case "$*" in
  "login status") echo "Not logged in"; exit 1;;
  *) echo 'stub codex 0.0.0';;
esac
`)
	writeExec(t, filepath.Join(bin, "gh"), `case "$*" in
  "auth status")
    echo "github.com" >&2
    echo "  ✓ Logged in to github.com account fake-user (keyring)" >&2
    echo "  - Token: `+fakeGHToken+`" >&2
    exit 0;;
esac
exit 3
`)
	writeExec(t, filepath.Join(bin, "git"), `echo "$*" >> `+gitCalls+`
case "$*" in
  *"remote get-url origin"*) echo "https://example.invalid/acme/repo.git"; exit 0;;
  *push*--dry-run*) echo "To https://example.invalid/acme/repo.git"; exit 0;;
  *push*) echo "refusing a real push in a test" >&2; exit 99;;
esac
exit 0
`)
	agents := filepath.Join(dir, ".itervox", "agents", "coder")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"SOUL.md", "INSTRUCTIONS.md"} {
		if err := os.WriteFile(filepath.Join(agents, f), []byte("# "+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ssh := ""
	if sshHosts != "" {
		ssh = "  ssh_hosts: [" + sshHosts + "]\n"
	}
	wf := filepath.Join(dir, "WORKFLOW.md")
	content := `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: lin_api_fakefakefakefakefakefakefakefake
  project_slug: acme
  endpoint: ` + linearURL + `
agent:
  command: ` + filepath.Join(bin, "claude") + `
` + ssh + `  profiles:
    coder:
      command: ` + filepath.Join(bin, "codex") + `
      soul_file: .itervox/agents/coder/SOUL.md
      instructions_file: .itervox/agents/coder/INSTRUCTIONS.md
---

Prompt.
`
	if err := os.WriteFile(wf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH", "HOME", daemonChildArgsEnv, "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN",
			"ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY", "CLAUDE_CONFIG_DIR",
			"LINEAR_API_KEY", "GITHUB_TOKEN", "GH_TOKEN":
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"PATH="+bin+":/usr/bin:/bin",
		"HOME="+filepath.Join(dir, "home"),
		"ANTHROPIC_API_KEY=sk-ant-fake",
	)
	return &doctorFixture{dir: dir, workflow: wf, bin: bin, gitCalls: gitCalls, env: env}
}

// runDoctorChild runs `itervox <args>` in a child test binary.
func (f *doctorFixture) runDoctorChild(t *testing.T, args ...string) (string, int) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0])
	cmd.Dir = f.dir
	cmd.Env = append(append([]string{}, f.env...), daemonChildArgsEnv+"="+string(raw))
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() { out, runErr = cmd.CombinedOutput(); close(done) }() // test-only
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("doctor child hung; output:\n%s", out)
	}
	code := 0
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		code = exitErr.ExitCode()
	} else if runErr != nil {
		t.Fatalf("doctor child: %v", runErr)
	}
	return string(out), code
}

func fakeLinear(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") == "" || !strings.Contains(string(body), "viewer") {
			http.Error(w, `{"errors":[{"message":"bad probe"}]}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"id":"user-1","name":"Fake User"}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fakeReady(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ready" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ready":` + strconv.FormatBool(status == 200) + `,"loop_fresh":true,"draining":false}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func lineWith(out, needle string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}

func TestDoctorDeployProbes(t *testing.T) {
	linear := fakeLinear(t)
	ready := fakeReady(t, http.StatusOK)
	f := newDoctorFixture(t, linear.URL, "")
	if err := os.MkdirAll(filepath.Join(f.dir, ".itervox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, ".itervox", "dashboard_url"), []byte(ready.URL+"/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := f.runDoctorChild(t, "doctor", "--deploy", "--workflow", f.workflow)
	t.Logf("doctor --deploy output (exit %d):\n%s", code, out)

	for needle, want := range map[string]string{
		"claude credentials": "[ok]",
		"codex credentials":  "[fail]",
		"gh auth":            "[ok]",
		"git push auth":      "[ok]",
		"tracker API":        "[ok]",
		"daemon /ready":      "[ok]",
	} {
		line := lineWith(out, needle)
		if line == "" {
			t.Errorf("no %q line in the output", needle)
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(line), want) {
			t.Errorf("%s line = %q, want status %s", needle, line, want)
		}
	}
	if code == 0 {
		t.Errorf("exit code 0 with a failing codex probe; want non-zero")
	}
	if strings.Contains(out, fakeGHToken) {
		t.Errorf("gh's token leaked into doctor output")
	}
	calls, _ := os.ReadFile(f.gitCalls)
	if !strings.Contains(string(calls), "push --dry-run") || !strings.Contains(string(calls), "refs/heads/itervox-doctor-probe") {
		t.Errorf("fake git never saw a dry-run push; calls:\n%s", calls)
	}
	for _, l := range strings.Split(string(calls), "\n") {
		if strings.Contains(l, "push") && !strings.Contains(l, "--dry-run") {
			t.Errorf("a push without --dry-run reached git: %q", l)
		}
	}
	if strings.Contains(out, "HEARTBEAT") && strings.Contains(out, "mtime") {
		t.Errorf("doctor --deploy must not probe HEARTBEAT mtime (00-core-project.md:46)")
	}
}

func TestDoctorDeploySkipsSSHHostCredentials(t *testing.T) {
	f := newDoctorFixture(t, fakeLinear(t).URL, `"build-1", "build-2"`)
	out, _ := f.runDoctorChild(t, "doctor", "--deploy", "--workflow", f.workflow)
	for _, host := range []string{"build-1", "build-2"} {
		for _, backend := range []string{"claude", "codex"} {
			found := false
			for _, l := range strings.Split(out, "\n") {
				if strings.HasPrefix(strings.TrimSpace(l), "[skipped]") && strings.Contains(l, backend+" credentials") && strings.Contains(l, host) {
					found = true
				}
			}
			if !found {
				t.Errorf("no skipped %s credentials line for ssh host %s:\n%s", backend, host, out)
			}
		}
	}
	if line := lineWith(out, "daemon /ready"); !strings.Contains(line, "[warn]") {
		t.Errorf("no dashboard_url: /ready should warn (daemon not running here), got %q", line)
	}
}

func TestDoctorUnknownFlagIsUsageError(t *testing.T) {
	f := newDoctorFixture(t, "http://127.0.0.1:1", "")
	out, code := f.runDoctorChild(t, "doctor", "--bogus")
	if code == 0 {
		t.Fatalf("doctor --bogus exited 0; output:\n%s", out)
	}
	if !strings.Contains(out, "unknown flag") || !strings.Contains(out, "usage: itervox doctor") {
		t.Errorf("want a usage error naming the flag, got:\n%s", out)
	}
}

func TestProbeLineRedactsTokens(t *testing.T) {
	got := probeLine([]byte("  - Token: "+fakeGHToken+"\nX error: bad credentials for Bearer abcdefghijklmnopqrstuvwxyz\n"), "error")
	if strings.Contains(got, fakeGHToken) || strings.Contains(got, "abcdefghijklmnop") {
		t.Fatalf("probeLine leaked a token: %q", got)
	}
	if !strings.Contains(got, "error: bad credentials") {
		t.Errorf("probeLine should pick the preferred line, got %q", got)
	}
	if only := probeLine([]byte("Token: " + fakeGHToken)); strings.Contains(only, fakeGHToken) {
		t.Errorf("a token-only line leaked: %q", only)
	}
}

func TestClaudeCredentialCheck(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	base := func(files map[string]string, envs map[string]string, goos string) deployProbeEnv {
		return deployProbeEnv{
			getenv: func(k string) string { return envs[k] },
			readFile: func(p string) ([]byte, error) {
				if v, ok := files[p]; ok {
					return []byte(v), nil
				}
				return nil, os.ErrNotExist
			},
			homeDir: "/home/itervox",
			goos:    goos,
			now:     func() time.Time { return now },
		}
	}
	credPath := "/home/itervox/.claude/.credentials.json"
	future, past := now.Add(time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli()
	for _, tc := range []struct {
		name  string
		env   deployProbeEnv
		inUse bool
		want  deployStatus
	}{
		{"not used", base(nil, nil, "linux"), false, deploySkipped},
		{"api key env", base(nil, map[string]string{"ANTHROPIC_API_KEY": "x"}, "linux"), true, deployOK},
		{"oauth token env", base(nil, map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "x"}, "linux"), true, deployOK},
		{"valid file", base(map[string]string{credPath: `{"claudeAiOauth":{"accessToken":"a","expiresAt":` + strconv.FormatInt(future, 10) + `}}`}, nil, "linux"), true, deployOK},
		{"expired with refresh", base(map[string]string{credPath: `{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":` + strconv.FormatInt(past, 10) + `}}`}, nil, "linux"), true, deployOK},
		{"expired no refresh", base(map[string]string{credPath: `{"claudeAiOauth":{"accessToken":"a","expiresAt":` + strconv.FormatInt(past, 10) + `}}`}, nil, "linux"), true, deployFail},
		{"file without login", base(map[string]string{credPath: `{}`}, nil, "linux"), true, deployFail},
		{"nothing on linux", base(nil, nil, "linux"), true, deployFail},
		{"nothing on macOS (Keychain)", base(nil, nil, "darwin"), true, deployWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := claudeCredentialCheck(tc.inUse, tc.env)
			if got.Status != tc.want {
				t.Errorf("status = %s (%s), want %s", got.Status, got.Detail, tc.want)
			}
		})
	}
}
