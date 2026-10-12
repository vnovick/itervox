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
	"os"
	"path/filepath"
	"strings"
	"time"
)

// daemonClient talks to a running daemon's API for the terminal commands
// that replace the TUI (#120): watch, logs, pause, resume, dispatch,
// workers, reply.
type daemonClient struct {
	base  string // e.g. http://127.0.0.1:8090 (no trailing slash)
	token string
	http  *http.Client
}

// daemonFlags registers the flags every client command takes.
type daemonFlags struct {
	workflow string
	logsDir  string
	url      string
}

func (f *daemonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.workflow, "workflow", "WORKFLOW.md", "path to WORKFLOW.md")
	fs.StringVar(&f.logsDir, "logs-dir", "", "the daemon's logs directory (default: the per-project dir under ~/.itervox/logs)")
	fs.StringVar(&f.url, "url", "", "the daemon's address (default: .itervox/dashboard_url)")
}

var errNoDaemon = errors.New("no running daemon found here: .itervox/dashboard_url is missing (start itervox, or pass --url)")

// client finds the daemon: --url or .itervox/dashboard_url, and the token:
// ITERVOX_API_TOKEN, then .itervox/.env, then <logs-dir>/api-token. A token
// is not required (server.allow_unauthenticated).
func (f *daemonFlags) client() (*daemonClient, error) {
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
	return &daemonClient{base: base, token: token, http: &http.Client{}}, nil
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
