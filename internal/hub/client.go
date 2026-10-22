package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/apitokens"
)

// TokenFunc returns the hub's stored token of scope (viewer or operator) for
// member m: a token in the member's own token store, created on first use
// and remembered 0600 in the member's .itervox directory, the way `itervox
// mcp` keeps its token (#216). The secret is never printed or logged.
type TokenFunc func(m MemberRecord, scope apitokens.Scope) (string, error)

// memberResponseLimit bounds a member response the hub reads itself (feed
// pages, summaries, route tables). Proxied responses stream unbounded.
const memberResponseLimit = 8 << 20

// tokenRetryDelay is how long the hub waits before retrying a request a
// member refused with 401: a token just created reaches the member's
// in-memory token index within ~200 ms (apitokens' mtime check).
const tokenRetryDelay = 300 * time.Millisecond

// memberClient calls members with the hub's member tokens, cached in memory.
type memberClient struct {
	http    *http.Client
	tokenFn TokenFunc

	mu    sync.Mutex
	cache map[string]string // workflow + "\x00" + scope → secret
}

func newMemberClient(fn TokenFunc) *memberClient {
	return &memberClient{
		http:    &http.Client{Timeout: 10 * time.Second},
		tokenFn: fn,
		cache:   map[string]string{},
	}
}

func tokenKey(m MemberRecord, scope apitokens.Scope) string {
	return m.Workflow + "\x00" + string(scope)
}

// tokenPropagation bounds how long the hub waits for a member to accept a
// member token it just obtained (the member re-reads tokens.json at most
// every 200 ms).
const tokenPropagation = 3 * time.Second

// token returns the member token of scope ("" for a member that needs none).
// A token not cached yet is first checked against the member's whoami, so
// a request never races the member's token-store refresh.
func (c *memberClient) token(ctx context.Context, m MemberRecord, scope apitokens.Scope) (string, error) {
	if !m.TokenRequired {
		return "", nil
	}
	if !scope.Valid() || scope == apitokens.ScopeAdmin {
		return "", fmt.Errorf("hub: no member token of scope %q", scope)
	}
	key := tokenKey(m, scope)
	c.mu.Lock()
	secret, ok := c.cache[key]
	c.mu.Unlock()
	if ok {
		return secret, nil
	}
	if c.tokenFn == nil {
		return "", errors.New("hub: no member token source")
	}
	secret, err := c.tokenFn(m, scope)
	if err != nil {
		return "", err
	}
	c.awaitAccepted(ctx, m, secret)
	c.mu.Lock()
	c.cache[key] = secret
	c.mu.Unlock()
	return secret, nil
}

// awaitAccepted polls the member's GET /api/v1/whoami with secret until it
// stops answering 401, for at most tokenPropagation.
func (c *memberClient) awaitAccepted(ctx context.Context, m MemberRecord, secret string) {
	deadline := time.Now().Add(tokenPropagation)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, memberURL(m, "/api/v1/whoami"), nil)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := c.http.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// invalidate drops a cached token the member refused; the next token call
// asks the TokenFunc again, which replaces a revoked or rotated token.
func (c *memberClient) invalidate(m MemberRecord, scope apitokens.Scope) {
	c.mu.Lock()
	delete(c.cache, tokenKey(m, scope))
	c.mu.Unlock()
}

// memberReply is a member response read in full.
type memberReply struct {
	status int
	header http.Header
	body   []byte
}

// get calls GET path (with query) on m with the token of scope. A 401 drops
// the cached token and retries once with a fresh one.
func (c *memberClient) get(ctx context.Context, m MemberRecord, scope apitokens.Scope, path string, hdr http.Header) (memberReply, error) {
	rep, err := c.getOnce(ctx, m, scope, path, hdr)
	if err != nil || rep.status != http.StatusUnauthorized || !m.TokenRequired {
		return rep, err
	}
	c.invalidate(m, scope)
	select {
	case <-ctx.Done():
		return rep, ctx.Err()
	case <-time.After(tokenRetryDelay):
	}
	return c.getOnce(ctx, m, scope, path, hdr)
}

func (c *memberClient) getOnce(ctx context.Context, m MemberRecord, scope apitokens.Scope, path string, hdr http.Header) (memberReply, error) {
	secret, err := c.token(ctx, m, scope)
	if err != nil {
		return memberReply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, memberURL(m, path), nil)
	if err != nil {
		return memberReply{}, fmt.Errorf("hub: member request: %w", err)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return memberReply{}, fmt.Errorf("hub: member %s unreachable: %w", m.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, memberResponseLimit))
	if err != nil {
		return memberReply{}, fmt.Errorf("hub: read member response: %w", err)
	}
	return memberReply{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// memberURL joins the member's base URL and an absolute path.
func memberURL(m MemberRecord, path string) string {
	return strings.TrimRight(m.URL, "/") + path
}
