package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/tracker/github"
	"github.com/vnovick/itervox/internal/tracker/linear"
)

// CORE-063 — `itervox doctor --deploy`: non-mutating deployment probes.
//
//   - agent credentials: claude (env, then the credentials file and its
//     expiry; the macOS Keychain is not read) and codex (env, then the
//     read-only `codex login status`); only for backends the config uses.
//     Workers on agent.ssh_hosts are not probed: one "skipped" line per host.
//   - `gh auth status` (read-only).
//   - push authentication: `git push --dry-run` to a probe ref — proves the
//     credentials, not branch protection or receive hooks.
//   - the tracker API: one read (Linear `{ viewer }`, GitHub GET /repos/…
//     with its push permission), through the adapters' own transports.
//   - the running daemon's /api/v1/ready (CORE-043).
//
// There is deliberately NO HEARTBEAT.md freshness probe: the heartbeat is
// written only when state changed and its interval elapsed, so its mtime is
// not liveness (00-core-project.md, reconciled line for CORE-063).
//
// Every external call goes through deployProbeEnv so tests substitute fakes;
// probed CLI output is redacted (logging.RedactString) and cut to one line
// before it is printed.

type deployStatus string

const (
	deployOK      deployStatus = "ok"
	deployWarn    deployStatus = "warn"
	deployFail    deployStatus = "fail"
	deploySkipped deployStatus = "skipped"
)

type deployCheck struct {
	Name   string
	Status deployStatus
	Detail string
}

// deployProbeEnv is the injectable seam for every external call.
type deployProbeEnv struct {
	// run executes a CLI with a timeout; it returns combined output.
	run      func(ctx context.Context, dir string, extraEnv []string, name string, args ...string) ([]byte, error)
	lookPath func(string) (string, error)
	getenv   func(string) string
	readFile func(string) ([]byte, error)
	homeDir  string
	goos     string
	now      func() time.Time
	http     *http.Client
	timeout  time.Duration
}

func defaultDeployProbeEnv() deployProbeEnv {
	home, _ := os.UserHomeDir()
	return deployProbeEnv{
		run: func(ctx context.Context, dir string, extraEnv []string, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), extraEnv...)
			cmd.Stdin = nil
			return cmd.CombinedOutput()
		},
		lookPath: exec.LookPath,
		getenv:   os.Getenv,
		readFile: os.ReadFile,
		homeDir:  home,
		goos:     runtime.GOOS,
		now:      time.Now,
		http:     &http.Client{Timeout: 10 * time.Second},
		timeout:  10 * time.Second,
	}
}

// probeLine redacts CLI output and keeps one informative line: the first
// line containing any of prefer (case-insensitive), else the first non-empty
// line. Never the raw output (tokens, hosts' full status dumps).
func probeLine(out []byte, prefer ...string) string {
	first, match := "", ""
	for _, l := range strings.Split(logging.RedactString(string(out)), "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "✓✗X-*!"))
		if l == "" {
			continue
		}
		if first == "" {
			first = l
		}
		if match == "" && slices.ContainsFunc(prefer, func(p string) bool {
			return strings.Contains(strings.ToLower(l), strings.ToLower(p))
		}) {
			match = l
		}
	}
	pick := or(match, first)
	if len(pick) > 160 {
		pick = pick[:160] + "…"
	}
	return pick
}

// runDeployChecks runs every deploy probe for cfg.
func runDeployChecks(ctx context.Context, cfg *config.Config, workflowPath string, env deployProbeEnv) []deployCheck {
	var out []deployCheck
	used := backendsInUse(cfg)
	out = append(out, claudeCredentialCheck(used["claude"], env))
	out = append(out, codexCredentialCheck(ctx, used["codex"], env))
	for _, host := range cfg.Agent.SSHHosts {
		for _, b := range []string{"claude", "codex"} {
			if used[b] {
				out = append(out, deployCheck{b + " credentials (ssh " + host + ")", deploySkipped,
					"not probed — doctor checks this machine only; run `itervox doctor --deploy` on " + host})
			}
		}
	}
	out = append(out, ghAuthCheck(ctx, env))
	out = append(out, gitPushCheck(ctx, filepath.Dir(workflowPath), env))
	out = append(out, trackerAPICheck(ctx, cfg, env))
	out = append(out, readyCheck(ctx, workflowPath, env))
	return out
}

// backendsInUse is the set of agent backends the default agent and every
// enabled profile dispatch to.
func backendsInUse(cfg *config.Config) map[string]bool {
	used := map[string]bool{configuredBackend(cfg.Agent.Command, cfg.Agent.Backend): true}
	for _, p := range cfg.Agent.Profiles {
		if config.ProfileEnabled(p) {
			used[configuredBackend(p.Command, p.Backend)] = true
		}
	}
	return used
}

// claudeCredentials is the subset of Claude Code's credentials file doctor
// reads (Linux/containers; macOS keeps it in the Keychain).
type claudeCredentials struct {
	ClaudeAiOauth *struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"` // unix ms
	} `json:"claudeAiOauth"`
}

func claudeCredentialCheck(inUse bool, env deployProbeEnv) deployCheck {
	c := deployCheck{Name: "claude credentials"}
	if !inUse {
		c.Status, c.Detail = deploySkipped, "no profile uses the claude backend"
		return c
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"} {
		if env.getenv(k) != "" {
			c.Status, c.Detail = deployOK, k+" is set (value not validated against the API)"
			return c
		}
	}
	for _, k := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
		if v := env.getenv(k); v != "" && v != "0" {
			c.Status, c.Detail = deployOK, k+" is set — cloud-provider credentials, not probed"
			return c
		}
	}
	dir := env.getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(env.homeDir, ".claude")
	}
	path := filepath.Join(dir, ".credentials.json")
	data, err := env.readFile(path)
	if err == nil {
		var creds claudeCredentials
		if json.Unmarshal(data, &creds) != nil || creds.ClaudeAiOauth == nil || creds.ClaudeAiOauth.AccessToken == "" {
			c.Status, c.Detail = deployFail, path+" has no OAuth login — run `claude` and log in, or set CLAUDE_CODE_OAUTH_TOKEN"
			return c
		}
		o := creds.ClaudeAiOauth
		expires := time.UnixMilli(o.ExpiresAt)
		switch {
		case o.ExpiresAt == 0 || env.now().Before(expires):
			c.Status, c.Detail = deployOK, "logged in ("+path+")"
		case o.RefreshToken != "":
			c.Status, c.Detail = deployOK, "access token expired at "+expires.UTC().Format(time.RFC3339)+"; the CLI renews it with the stored refresh token ("+path+")"
		default:
			c.Status, c.Detail = deployFail, "login expired at "+expires.UTC().Format(time.RFC3339)+" and no refresh token — run `claude` and log in again"
		}
		return c
	}
	if env.goos == "darwin" {
		c.Status, c.Detail = deployWarn, "no credential env var or "+path+"; on macOS Claude Code keeps its login in the Keychain, which doctor does not read — run `claude` once as the daemon's user to confirm"
		return c
	}
	c.Status, c.Detail = deployFail, "no ANTHROPIC_API_KEY / CLAUDE_CODE_OAUTH_TOKEN and no "+path+" — set one (e.g. `claude setup-token`) or log in with `claude`"
	return c
}

func codexCredentialCheck(ctx context.Context, inUse bool, env deployProbeEnv) deployCheck {
	c := deployCheck{Name: "codex credentials"}
	if !inUse {
		c.Status, c.Detail = deploySkipped, "no profile uses the codex backend"
		return c
	}
	for _, k := range []string{"CODEX_API_KEY", "OPENAI_API_KEY"} {
		if env.getenv(k) != "" {
			c.Status, c.Detail = deployOK, k+" is set (value not validated against the API)"
			return c
		}
	}
	bin, err := env.lookPath("codex")
	if err != nil {
		c.Status, c.Detail = deployFail, "codex CLI not on PATH and no CODEX_API_KEY / OPENAI_API_KEY"
		return c
	}
	cctx, cancel := context.WithTimeout(ctx, env.timeout)
	defer cancel()
	out, err := env.run(cctx, "", nil, bin, "login", "status") // read-only
	line := probeLine(out, "logged in", "not logged")
	if err != nil {
		c.Status, c.Detail = deployFail, "`codex login status`: "+or(line, err.Error())+" — run `codex login` or set CODEX_API_KEY"
		return c
	}
	c.Status, c.Detail = deployOK, "`codex login status`: "+or(line, "logged in")
	return c
}

func ghAuthCheck(ctx context.Context, env deployProbeEnv) deployCheck {
	c := deployCheck{Name: "gh auth"}
	bin, err := env.lookPath("gh")
	if err != nil {
		c.Status, c.Detail = deployWarn, "gh not on PATH — PR detection and merge actions will not work"
		return c
	}
	cctx, cancel := context.WithTimeout(ctx, env.timeout)
	defer cancel()
	out, err := env.run(cctx, "", []string{"GH_PROMPT_DISABLED=1"}, bin, "auth", "status") // read-only
	line := probeLine(out, "logged in", "not logged", "failed")
	if err != nil {
		c.Status, c.Detail = deployFail, "`gh auth status`: "+or(line, err.Error())+" — run `gh auth login` or set GH_TOKEN"
		return c
	}
	c.Status, c.Detail = deployOK, "`gh auth status`: "+or(line, "authenticated")
	return c
}

// doctorProbeRef is the ref the dry-run push targets; --dry-run never
// creates it.
const doctorProbeRef = "refs/heads/itervox-doctor-probe"

func gitPushCheck(ctx context.Context, dir string, env deployProbeEnv) deployCheck {
	c := deployCheck{Name: "git push auth"}
	bin, err := env.lookPath("git")
	if err != nil {
		c.Status, c.Detail = deployFail, "git not on PATH"
		return c
	}
	noPrompt := []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never"}
	cctx, cancel := context.WithTimeout(ctx, env.timeout)
	defer cancel()
	if out, err := env.run(cctx, dir, noPrompt, bin, "remote", "get-url", "origin"); err != nil {
		c.Status, c.Detail = deploySkipped, "no `origin` remote in "+dir+": "+or(probeLine(out), err.Error())
		return c
	}
	// --no-verify (M4-close D9 / BH-M4-1): --dry-run still runs the local
	// pre-push hook, and lefthook/husky suites are arbitrary code that can
	// take minutes — a read-only probe must not execute them, and a failing
	// hook must not read as a push-auth failure.
	out, err := env.run(cctx, dir, noPrompt, bin, "push", "--dry-run", "--no-verify", "--porcelain", "origin", "HEAD:"+doctorProbeRef)
	if err != nil {
		c.Status, c.Detail = deployFail, "`git push --dry-run --no-verify origin HEAD:"+doctorProbeRef+"`: "+or(probeLine(out, "denied", "error", "fatal"), err.Error())
		return c
	}
	c.Status, c.Detail = deployOK, "push auth ok (dry-run to "+doctorProbeRef+"; branch protection and receive hooks are not exercised)"
	return c
}

func trackerAPICheck(ctx context.Context, cfg *config.Config, env deployProbeEnv) deployCheck {
	c := deployCheck{Name: "tracker API"}
	cctx, cancel := context.WithTimeout(ctx, env.timeout)
	defer cancel()
	switch cfg.Tracker.Kind {
	case "linear":
		if cfg.Tracker.APIKey == "" {
			c.Status, c.Detail = deployFail, "linear: tracker.api_key is empty (is LINEAR_API_KEY set?)"
			return c
		}
		who, err := linear.NewClient(linear.ClientConfig{APIKey: cfg.Tracker.APIKey, Endpoint: cfg.Tracker.Endpoint}).ProbeViewer(cctx)
		if err != nil {
			c.Status, c.Detail = deployFail, "linear `{ viewer }` query failed: "+logging.RedactString(err.Error())
			return c
		}
		c.Status, c.Detail = deployOK, "linear token valid (viewer: "+who+")"
	case "github":
		if cfg.Tracker.APIKey == "" {
			c.Status, c.Detail = deployFail, "github: tracker.api_key is empty (is GITHUB_TOKEN set?)"
			return c
		}
		push, err := github.NewClient(github.ClientConfig{APIKey: cfg.Tracker.APIKey, ProjectSlug: cfg.Tracker.ProjectSlug, Endpoint: cfg.Tracker.Endpoint}).ProbeRepoAccess(cctx)
		if err != nil {
			c.Status, c.Detail = deployFail, "github GET /repos/"+cfg.Tracker.ProjectSlug+" failed: "+logging.RedactString(err.Error())
			return c
		}
		if !push {
			c.Status, c.Detail = deployWarn, "github token can read "+cfg.Tracker.ProjectSlug+" but has no push (write) permission — label and state changes will fail"
			return c
		}
		c.Status, c.Detail = deployOK, "github token can read and write "+cfg.Tracker.ProjectSlug
	default:
		c.Status, c.Detail = deploySkipped, "tracker kind "+cfg.Tracker.Kind+" has no remote API"
	}
	return c
}

func readyCheck(ctx context.Context, workflowPath string, env deployProbeEnv) deployCheck {
	c := deployCheck{Name: "daemon /ready"}
	data, err := env.readFile(dashboardURLFilePath(workflowPath))
	if err != nil {
		c.Status, c.Detail = deployWarn, "no running daemon here (no .itervox/dashboard_url) — start it, then re-run to probe /api/v1/ready"
		return c
	}
	base := strings.TrimSpace(string(data))
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	cctx, cancel := context.WithTimeout(ctx, env.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, base+"api/v1/ready", nil)
	if err != nil {
		c.Status, c.Detail = deployFail, "bad dashboard URL "+base+": "+err.Error()
		return c
	}
	resp, err := env.http.Do(req)
	if err != nil {
		c.Status, c.Detail = deployFail, base+"api/v1/ready unreachable: "+err.Error()
		return c
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var ready map[string]any
	_ = json.Unmarshal(body, &ready)
	flags := []string{}
	for _, k := range []string{"loop_fresh", "last_poll_ok", "config_invalid", "degraded", "draining"} {
		if v, ok := ready[k].(bool); ok {
			flags = append(flags, fmt.Sprintf("%s=%v", k, v))
		}
	}
	if resp.StatusCode == http.StatusOK {
		c.Status, c.Detail = deployOK, "ready ("+strings.Join(flags, " ")+")"
		return c
	}
	c.Status, c.Detail = deployFail, fmt.Sprintf("HTTP %d not ready (%s)", resp.StatusCode, strings.Join(flags, " "))
	return c
}

// renderDeployChecks formats one "[status] name: detail" line per check and
// returns the exit code: 1 when any check failed.
func renderDeployChecks(checks []deployCheck) (string, int) {
	var b strings.Builder
	b.WriteString("\ndeploy checks (--deploy)\n------------------------\n")
	code := 0
	for _, c := range checks {
		fmt.Fprintf(&b, "%-10s %s: %s\n", "["+string(c.Status)+"]", c.Name, c.Detail)
		if c.Status == deployFail {
			code = 1
		}
	}
	failed := slices.IndexFunc(checks, func(c deployCheck) bool { return c.Status == deployFail }) >= 0
	if failed {
		b.WriteString("deploy: FAILED — fix the [fail] lines above\n")
	} else {
		b.WriteString("deploy: OK\n")
	}
	return b.String(), code
}

// runDeployDoctor loads cfg and runs the deploy probes. A config that does
// not load is a failure of its own (reported by the base doctor too).
func runDeployDoctor(workflowPath string, env deployProbeEnv) (string, int) {
	cfg, err := config.Load(workflowPath)
	if err != nil {
		return renderDeployChecks([]deployCheck{{"workflow", deployFail, "cannot load " + workflowPath + ": " + err.Error()}})
	}
	return renderDeployChecks(runDeployChecks(context.Background(), cfg, workflowPath, env))
}

// loadDotEnvFrom loads <dir>/.itervox/.env like the daemon does at startup
// (godotenv never overrides a variable that is already set). A missing file
// is fine.
func loadDotEnvFrom(dir string) {
	p := filepath.Join(dir, ".itervox", ".env")
	if _, err := os.Stat(p); err != nil {
		return
	}
	if err := godotenv.Load(p); err != nil {
		fmt.Fprintf(os.Stderr, "doctor: could not load %s: %v\n", p, err)
	}
}
