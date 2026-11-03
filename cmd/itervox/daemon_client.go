package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/hub"
)

// daemonClient talks to a running daemon's API for the terminal commands
// (#120, #240): watch, logs, pause, resume, dispatch, workers, reply,
// approve.
type daemonClient struct {
	base  string // e.g. http://127.0.0.1:8090 (no trailing slash)
	token string
	http  *http.Client
	// discover finds the daemon again; nil with --url. A daemon restarted
	// with server.port: 0 listens on a new port and rewrites
	// .itervox/dashboard_url (and may have a new token), so a client that
	// reconnects calls rediscover first.
	discover func() (*daemonClient, error)
}

// rediscover re-reads the daemon's address and token, when they were
// discovered rather than given with --url. On failure the last known ones
// stay.
func (c *daemonClient) rediscover() {
	if c.discover == nil {
		return
	}
	if fresh, err := c.discover(); err == nil {
		c.base, c.token = fresh.base, fresh.token
	}
}

// daemonFlags registers the flags every client command takes.
type daemonFlags struct {
	workflow string
	logsDir  string
	url      string
	// project (#240) reaches one project through the machine's hub
	// (/hub/p/{project}/api/v1/...) with a hub token; url is then the hub's
	// address.
	project string
}

func (f *daemonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.workflow, "workflow", "WORKFLOW.md", "path to WORKFLOW.md")
	fs.StringVar(&f.logsDir, "logs-dir", "", "the daemon's logs directory (default: the per-project dir under ~/.itervox/logs)")
	fs.StringVar(&f.url, "url", "", "the daemon's address (default: .itervox/dashboard_url); with --project, the hub's")
	fs.StringVar(&f.project, "project", "", "reach this hub project (itervox hub lists them) through the machine's hub, with the hub token in "+hubTokenEnv)
}

// hubTokenEnv names the hub token the client commands send with --project
// (#240; create one with itervox token create --hub).
const hubTokenEnv = "ITERVOX_HUB_TOKEN"

// targetArgs are the flags that make another client command reach the same
// daemon or hub project, for the commands watch prints. Never a token.
func (f *daemonFlags) targetArgs() []string {
	var args []string
	if f.workflow != "" && f.workflow != "WORKFLOW.md" {
		args = append(args, "--workflow", f.workflow)
	}
	if f.url != "" {
		args = append(args, "--url", f.url)
	}
	if f.project != "" {
		args = append(args, "--project", f.project)
	}
	return args
}

var errNoHub = errors.New("no itervox hub is running on this machine (itervox hub shows why), or pass the hub's address with --url")

// hubClient reaches project through the hub: --url, else the hub lock's
// address; the token is $ITERVOX_HUB_TOKEN.
func (f *daemonFlags) hubClient() (*daemonClient, error) {
	base := f.url
	if base == "" {
		dir, err := daemonRegistryDir()
		if err != nil {
			return nil, err
		}
		info, ok, _ := hub.ReadLockInfo(dir)
		if !ok {
			return nil, errNoHub
		}
		base = info.URL
	}
	if i := strings.Index(base, "?"); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/hub")
	c := &daemonClient{base: base + "/hub/p/" + url.PathEscape(f.project), token: os.Getenv(hubTokenEnv), http: &http.Client{}}
	if f.url == "" {
		again := *f
		c.discover = again.hubClient // a hub failover moves the hub
	}
	return c, nil
}

var errNoDaemon = errors.New("no running daemon found here: .itervox/dashboard_url is missing (start itervox, or pass --url)")

// client finds the daemon: --url or .itervox/dashboard_url, and the token:
// ITERVOX_API_TOKEN, then .itervox/.env, then <logs-dir>/api-token. A token
// is not required (server.allow_unauthenticated).
func (f *daemonFlags) client() (*daemonClient, error) {
	if f.project != "" {
		return f.hubClient()
	}
	base := f.url
	if base == "" {
		raw, err := os.ReadFile(dashboardURLFilePath(f.workflow))
		if err != nil {
			return nil, errNoDaemon
		}
		base = strings.TrimSpace(string(raw))
	}
	if i := strings.Index(base, "?"); i >= 0 {
		base = base[:i] // a tokenised URL
	}
	base = strings.TrimSuffix(base, "/")
	token := os.Getenv("ITERVOX_API_TOKEN")
	if token == "" {
		if raw, err := os.ReadFile(filepath.Join(filepath.Dir(f.workflow), ".itervox", ".env")); err == nil {
			token, _ = envFileValue(string(raw), "ITERVOX_API_TOKEN")
		}
	}
	if token == "" {
		dir := f.logsDir
		if dir == "" {
			dir = defaultLogsDir(f.workflow)
		}
		if raw, err := os.ReadFile(filepath.Join(dir, apiTokenFileName)); err == nil {
			token = strings.TrimSpace(string(raw))
		}
	}
	c := &daemonClient{base: base, token: token, http: &http.Client{}}
	if f.url == "" {
		again := *f
		c.discover = again.client
	}
	return c, nil
}

// apiError is a non-2xx answer; message is the API's error message.
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("the daemon answered %d", e.status)
	}
	return fmt.Sprintf("%s (%d)", e.message, e.status)
}

// request sends method path with body (JSON, nil for none) and decodes a
// 2xx answer into out (nil to ignore it).
func (c *daemonClient) request(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1"+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("the daemon at %s is not answering: %w", c.base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return readAPIError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *daemonClient) authorize(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func readAPIError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := ""
	if json.Unmarshal(raw, &body) == nil {
		msg = body.Error.Message
	}
	if msg == "" && resp.StatusCode == http.StatusUnauthorized {
		msg = "the daemon refused the API token (set ITERVOX_API_TOKEN, or check <logs-dir>/api-token)"
	}
	return &apiError{status: resp.StatusCode, message: msg}
}

// clientTimeout bounds one quick-action request.
const clientTimeout = 15 * time.Second
