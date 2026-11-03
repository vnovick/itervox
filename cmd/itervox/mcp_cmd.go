package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/mcpserver"
)

// `itervox mcp` (#216) is a local stdio MCP server for the board and memory
// of the running daemon of this project. It is an HTTP client of the
// daemon's API: it finds the daemon the way `itervox status --json` does
// (the host registry, then a process scan, then .itervox/dashboard_url) and
// authenticates with a dedicated stored token (mcp_token.go) unless one is
// given. Stdout carries MCP protocol messages only; diagnostics go to
// stderr.

const mcpUsage = `usage: itervox mcp [--project DIR | --workflow WORKFLOW.md] [--url URL] [--token-scope viewer|operator]
  Serves MCP over stdio for the running daemon of this project.
  Token: --token, else ITERVOX_TOKEN, else a stored "mcp" token created on
  first use and remembered in .itervox/mcp-token.json (0600, gitignored).`

// mcpTokenEnv names an explicit token for `itervox mcp`.
const mcpTokenEnv = "ITERVOX_TOKEN"

func runMCP(args []string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fatalExit(mcpCmd(ctx, args, os.Stdin, os.Stdout, os.Stderr, defaultMCPEnv()))
}

// mcpEnv is what `itervox mcp` reads from the host, so tests can replace
// it.
type mcpEnv struct {
	getenv      func(string) string
	registryDir string
	scan        func() []int
	cwd         func(int) string
}

func defaultMCPEnv() mcpEnv {
	dir, _ := daemonRegistryDir()
	return mcpEnv{getenv: os.Getenv, registryDir: dir, scan: discoverAllDaemons, cwd: processCwd}
}

// mcpCmd runs `itervox mcp` until the client closes stdin (or ctx ends) and
// returns the exit code.
func mcpCmd(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, env mcpEnv) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(errOut)
	workflow := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md (selects the project)")
	project := fs.String("project", "", "the project directory whose daemon to use (default: the --workflow directory)")
	urlFlag := fs.String("url", "", "the daemon's address, e.g. http://127.0.0.1:8090 (default: discovered)")
	token := fs.String("token", "", "API token to use (default: $"+mcpTokenEnv+", else the stored mcp token)")
	scopeFlag := fs.String("token-scope", "", "viewer (default) or operator: the scope of the stored mcp token, and the most the tools may do")
	fs.Usage = func() { _, _ = fmt.Fprintln(errOut, mcpUsage); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintln(errOut, "itervox mcp: "+err.Error())
		return 1
	}
	scope := apitokens.ScopeViewer
	if *scopeFlag != "" {
		s, err := apitokens.ParseScope(*scopeFlag)
		if err != nil || s == apitokens.ScopeAdmin {
			return fail(errors.New("--token-scope must be viewer or operator"))
		}
		scope = s
	}
	projectDir := *project
	if projectDir == "" {
		projectDir, _ = resolveProjectDir(*workflow)
	}
	if abs, err := filepath.Abs(projectDir); err == nil {
		projectDir = abs
	}
	target, err := findMCPDaemon(projectDir, *urlFlag, env)
	if err != nil {
		return fail(err)
	}
	secret, created := cmp.Or(*token, env.getenv(mcpTokenEnv)), false
	maxScope := string(scope)
	switch {
	case secret != "":
		if *scopeFlag == "" {
			maxScope = "" // the given token's own scope
		}
	case !target.tokenRequired:
		// server.allow_unauthenticated: every caller is admin, so the
		// --token-scope cap is all that keeps write tools away.
	default:
		secret, created, err = ensureMCPToken(target.workflow, scope)
		if err != nil {
			return fail(err)
		}
	}
	logging.RegisterSecret(secret)
	client := mcpserver.NewClient(target.base, secret)
	if created {
		waitForMCPToken(ctx, client)
	}
	if *urlFlag == "" {
		client.Rediscover = func() (string, error) {
			t, err := findMCPDaemon(projectDir, "", env)
			return t.base, err
		}
	}
	slog.Info("mcp: serving over stdio", "daemon", target.base, "project", projectDir, "max_scope", cmp.Or(maxScope, "token"))
	srv := &mcpserver.Server{API: client, Version: version, MaxScope: maxScope, Logger: slog.Default()}
	if got, err := srv.Scope(ctx); err != nil {
		slog.Warn("mcp: the daemon cannot be read yet; tools report it until it can", "error", err)
	} else {
		slog.Info("mcp: tools offered", "scope", got, "tools", strings.Join(mcpserver.ToolNames(got), ","))
	}
	if err := srv.Serve(ctx, in, out); err != nil && !errors.Is(err, context.Canceled) {
		return fail(err)
	}
	return 0
}

// mcpTarget is the daemon `itervox mcp` talks to.
type mcpTarget struct {
	base          string
	workflow      string
	tokenRequired bool
}

// findMCPDaemon selects the daemon: --url, else the registry/process-scan
// record whose project is projectDir (the newest when several), else
// projectDir/.itervox/dashboard_url.
func findMCPDaemon(projectDir, urlFlag string, env mcpEnv) (mcpTarget, error) {
	t := mcpTarget{workflow: filepath.Join(projectDir, "WORKFLOW.md"), tokenRequired: true}
	if urlFlag != "" {
		t.base = trimDaemonURL(urlFlag)
		return t, nil
	}
	records := collectDaemons(env.registryDir, env.scan, env.cwd)
	records = slices.DeleteFunc(records, func(r daemonRecord) bool { return !pathEquals(r.ProjectDir, projectDir) || r.URL == "" })
	if len(records) > 0 {
		slices.SortStableFunc(records, func(a, b daemonRecord) int { return strings.Compare(b.StartedAt, a.StartedAt) })
		r := records[0]
		if len(records) > 1 {
			slog.Warn("mcp: several daemons serve this project; using the newest", "pid", r.PID, "url", r.URL)
		}
		t.base, t.workflow, t.tokenRequired = trimDaemonURL(r.URL), r.Workflow, r.TokenRequired
		return t, nil
	}
	raw, err := os.ReadFile(dashboardURLFilePath(t.workflow))
	if err != nil {
		return t, fmt.Errorf("no running itervox daemon for %s (start itervox there, or pass --project or --url)", projectDir)
	}
	t.base = trimDaemonURL(string(raw))
	return t, nil
}

// trimDaemonURL drops a tokenised query and the trailing slash.
func trimDaemonURL(u string) string {
	u = strings.TrimSpace(u)
	u, _, _ = strings.Cut(u, "?")
	return strings.TrimSuffix(u, "/")
}
