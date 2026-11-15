package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/logging"
)

// requestTimeout bounds one API call.
const requestTimeout = 15 * time.Second

// maxResponseBytes bounds one API answer the server reads.
const maxResponseBytes = 8 << 20

// Client calls a daemon's /api/v1 with a bearer token.
type Client struct {
	// HTTP is the client used; nil is a default client.
	HTTP *http.Client
	// Rediscover finds the daemon again after a connection failure (a
	// daemon restarted with server.port: 0 listens on a new port); nil
	// keeps the address. It returns the new base URL.
	Rediscover func() (string, error)

	mu    sync.Mutex
	base  string
	token string
}

// NewClient returns a client for the daemon at base (e.g.
// http://127.0.0.1:8090) using token ("" for a daemon without
// authentication).
func NewClient(base, token string) *Client {
	return &Client{base: strings.TrimSuffix(base, "/"), token: token}
}

// APIError is a non-2xx answer of the daemon.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	switch {
	case e.Status == http.StatusUnauthorized:
		return "the daemon refused the token (401): it was revoked or rotated; restart the MCP server to mint a new one"
	case e.Status == http.StatusForbidden && e.Code == "insufficient_scope":
		return "the token's scope does not allow this (403)"
	case e.Message != "":
		return fmt.Sprintf("%s (%d)", e.Message, e.Status)
	}
	return fmt.Sprintf("the daemon answered %d", e.Status)
}

// Do sends method /api/v1+path with query and a JSON body (nil for none)
// and decodes a 2xx answer into out (nil to ignore it).
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	resp, err := c.send(ctx, method, path, query, raw)
	if err != nil && c.Rediscover != nil && !errors.Is(err, context.Canceled) {
		if base, rerr := c.Rediscover(); rerr == nil && base != "" {
			c.mu.Lock()
			c.base = strings.TrimSuffix(base, "/")
			c.mu.Unlock()
			resp, err = c.send(ctx, method, path, query, raw)
		}
	}
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("mcpserver: read the daemon's answer: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return &APIError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("mcpserver: decode %s %s: %w", method, path, err)
	}
	return nil
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, body []byte) (*http.Response, error) {
	c.mu.Lock()
	base, token := c.base, c.token
	c.mu.Unlock()
	u := base + "/api/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		cancel()
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("the daemon at %s is not answering: %w", base, err)
	}
	resp.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// allowed reports whether a caller of scope may use a tool requiring
// required.
func allowed(scope, required string) bool {
	return apitokens.Scope(scope).Allows(apitokens.Scope(required))
}

// capScope lowers scope to limit when limit is set and lower.
func capScope(scope, limit string) string {
	if limit == "" || !apitokens.Scope(limit).Valid() {
		return scope
	}
	if apitokens.Scope(limit).Allows(apitokens.Scope(scope)) {
		return scope
	}
	return limit
}

// redact scrubs secrets from text a tool returns.
func redact(s string) string { return logging.RedactString(s) }

// clip redacts s and cuts it to at most n bytes (on a rune boundary).
func clip(s string, n int) string {
	s = redact(strings.TrimSpace(s))
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
