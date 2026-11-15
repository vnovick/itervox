package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/orchestrator"
)

// `itervox open` opens the project daemon's dashboard in the browser, logged
// in. The dashboard's AuthGate (web/src/auth/AuthGate.tsx) takes ?token= from
// the address once, stores it in sessionStorage and strips it from the
// address bar, so a tokenised URL opened once is the whole hand-over. The
// daemon and its token are found as the terminal commands find them
// (daemonFlags.client: .itervox/dashboard_url, then ITERVOX_API_TOKEN,
// .itervox/.env, <logs-dir>/api-token), with the host registry's token_file
// for a daemon started with its own --logs-dir. The token is never logged:
// it goes to the OS opener's argv, or to stdout with --print.

const openUsage = `Usage: itervox open [--print] [--workflow PATH] [--logs-dir DIR] [--url URL]

Opens the running daemon's dashboard in your browser, logged in: the URL
carries the dashboard's API token as ?token=, which the dashboard stores and
removes from the address bar. --print prints that URL instead of opening it;
it contains a credential, so do not paste it into chats, issues or logs.

Flags:`

// openHealthTimeout bounds the check that the daemon is answering.
const openHealthTimeout = 3 * time.Second

func runOpen(args []string) {
	fatalExit(openCommand(args, os.Stdout, os.Stderr, openInBrowser))
}

// openCommand runs `itervox open` and returns its exit code: 0 opened (or
// printed), 1 no daemon / not answering / no opener, 2 usage. opener opens a
// URL in the browser.
func openCommand(args []string, out, errOut io.Writer, opener func(string) error) int {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var df daemonFlags
	fs.StringVar(&df.workflow, "workflow", "WORKFLOW.md", "path to WORKFLOW.md")
	fs.StringVar(&df.logsDir, "logs-dir", "", "the daemon's logs directory, where an auto-generated token is (default: the per-project dir under ~/.itervox/logs)")
	fs.StringVar(&df.url, "url", "", "the daemon's address (default: .itervox/dashboard_url)")
	printOnly := fs.Bool("print", false, "print the logged-in URL (it contains the API token) instead of opening a browser")
	fs.Usage = func() { _, _ = fmt.Fprintln(errOut, openUsage); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintln(errOut, "itervox open: "+err.Error())
		return 1
	}
	c, err := df.client()
	if err != nil {
		return fail(err)
	}
	if err := probeOpenTarget(c.base); err != nil {
		return fail(err)
	}
	token := c.token
	if token == "" && df.logsDir == "" {
		token = registryTokenForProject(df.workflow)
	}
	target := dashboardLoginURL(c.base, token)
	if *printOnly {
		_, _ = fmt.Fprintln(out, target)
		if token != "" {
			_, _ = fmt.Fprintln(errOut, "itervox open: the URL above contains a credential (the dashboard's API token); do not paste it into chats, issues or logs")
		}
		return 0
	}
	if err := opener(target); err != nil {
		return fail(fmt.Errorf("could not open a browser (%w); run `itervox open --print` and open the URL yourself", err))
	}
	how := "logged in"
	if token == "" {
		how = "no API token found; the dashboard asks for one unless the daemon runs without (server.allow_unauthenticated)"
	}
	_, _ = fmt.Fprintf(out, "itervox open: opened %s/ in your browser (%s)\n", c.base, how)
	return 0
}

// dashboardLoginURL is the dashboard address carrying token as ?token=
// (none without a token).
func dashboardLoginURL(base, token string) string {
	u := strings.TrimSuffix(base, "/") + "/"
	if token == "" {
		return u
	}
	return u + "?token=" + url.QueryEscape(token)
}

// probeOpenTarget checks that a daemon answers at base (GET /api/v1/health,
// public), so a stale dashboard_url is reported instead of opened.
func probeOpenTarget(base string) error {
	client := &http.Client{Timeout: openHealthTimeout}
	resp, err := client.Get(base + "/api/v1/health")
	if err != nil {
		return fmt.Errorf("the daemon at %s is not answering (%w); start it with `itervox`, or check `itervox status`", base, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the daemon at %s is not answering (health check returned %d)", base, resp.StatusCode)
	}
	return nil
}

// registryTokenForProject reads the auto-generated token of the project's
// daemon from the token_file its host-registry record names: set only for
// a generated token, and the only place it is when that daemon was started
// with its own --logs-dir. "" when there is none.
func registryTokenForProject(workflowPath string) string {
	dir, err := daemonRegistryDir()
	if err != nil {
		return ""
	}
	projectDir, _ := resolveProjectDir(workflowPath)
	for _, rec := range readDaemonRegistry(dir) {
		if rec.TokenSource != tokenSourceGenerated || rec.TokenFile == "" || !pathEquals(rec.ProjectDir, projectDir) {
			continue
		}
		if raw, err := os.ReadFile(rec.TokenFile); err == nil {
			return strings.TrimSpace(string(raw))
		}
	}
	return ""
}

// browserOpenerArgs is the OS command that opens u in the default browser.
func browserOpenerArgs(goos, u string) []string {
	switch goos {
	case "darwin":
		return []string{"open", u}
	case "windows":
		// Not `cmd /c start`: cmd.exe would treat the URL's & as a
		// command separator.
		return []string{"rundll32", "url.dll,FileProtocolHandler", u}
	default:
		return []string{"xdg-open", u}
	}
}

// openerWait is how long openInBrowser waits for the opener to fail before
// taking it as started: xdg-open can stay up as long as the browser it
// launched.
const openerWait = 3 * time.Second

// openInBrowser runs the OS opener for u. An opener that fails within
// openerWait is an error; one still running then is taken as working (it
// outlives this command). Errors never carry u: it holds the token.
func openInBrowser(u string) error {
	argv := browserOpenerArgs(runtime.GOOS, u)
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("%s not found", argv[0])
	}
	cmd := exec.Command(path, argv[1:]...)
	if err := cmd.Start(); err != nil {
		return errors.New(filepath.Base(argv[0]) + " did not start")
	}
	done := make(chan error, 1)
	go func() {
		defer orchestrator.RecoverGoroutine("open-browser-wait", "", nil)
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		if err != nil {
			return errors.New(filepath.Base(argv[0]) + " failed: " + err.Error())
		}
	case <-time.After(openerWait):
	}
	return nil
}
