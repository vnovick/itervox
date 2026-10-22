package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/mcpserver"
)

// The stored token of `itervox mcp` (#216). On first use it creates a token
// named "mcp" through the token store (.itervox/tokens.json, hash only) and
// remembers the secret in the project's .itervox/mcp-token.json: mode 0600,
// gitignored (the runtime "/*.json" line plus an explicit
// "/mcp-token.json"), next to the store it belongs to, so each project has
// its own. The secret is never printed or logged. A remembered token that
// was revoked, expired, rotated or has another scope is replaced (the old
// one revoked when it is still stored).

// mcpTokenFileName is the remembered token's file inside .itervox.
const mcpTokenFileName = "mcp-token.json"

// mcpTokenName names the stored token; mcpTokenActor the audit actor.
const (
	mcpTokenName  = "mcp"
	mcpTokenActor = "itervox mcp"
)

// mcpTokenFile is the shape of .itervox/mcp-token.json.
type mcpTokenFile struct {
	ID     string          `json:"id"`
	Scope  apitokens.Scope `json:"scope"`
	Secret string          `json:"secret"`
}

func mcpTokenPath(workflowPath string) string {
	return filepath.Join(filepath.Dir(workflowPath), ".itervox", mcpTokenFileName)
}

// ensureMCPToken returns the secret of the project's mcp token of scope,
// creating (and remembering) one when there is no usable one; created
// reports a new token.
func ensureMCPToken(workflowPath string, scope apitokens.Scope) (secret string, created bool, err error) {
	store := apitokens.Open(apitokens.Path(workflowPath))
	path := mcpTokenPath(workflowPath)
	var old mcpTokenFile
	raw, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		if json.Unmarshal(raw, &old) != nil {
			old = mcpTokenFile{}
		}
	case !errors.Is(rerr, fs.ErrNotExist):
		return "", false, fmt.Errorf("read %s: %w", path, rerr)
	}
	tokens, err := store.List()
	if err != nil {
		return "", false, fmt.Errorf("read the token store: %w", err)
	}
	stored := slices.IndexFunc(tokens, func(t apitokens.Token) bool { return old.ID != "" && t.ID == old.ID })
	if stored >= 0 {
		t := tokens[stored]
		if old.Scope == scope && t.Scope == scope && !t.Expired(time.Now()) && t.Hash == apitokens.HashSecret(old.Secret) {
			return old.Secret, false, nil
		}
	}
	t, secret, err := store.Create(mcpTokenName, scope, nil, mcpTokenActor)
	if err != nil {
		return "", false, fmt.Errorf("create the mcp token: %w", err)
	}
	if err := ensureItervoxGitignore(filepath.Dir(path)); err != nil {
		slog.Warn("mcp: could not make sure .itervox/mcp-token.json is gitignored", "error", err)
	}
	data, _ := json.MarshalIndent(mcpTokenFile{ID: t.ID, Scope: scope, Secret: secret}, "", "  ")
	if err := atomicfs.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		_, _ = store.Revoke(t.ID, mcpTokenActor) // never leave an unreachable token behind
		return "", false, fmt.Errorf("remember the mcp token in %s: %w", path, err)
	}
	if stored >= 0 && tokens[stored].Name == mcpTokenName {
		if _, err := store.Revoke(old.ID, mcpTokenActor); err != nil {
			slog.Warn("mcp: could not revoke the replaced mcp token", "id", old.ID, "error", err)
		}
	}
	slog.Info("mcp: created a stored token for the MCP server (itervox token list shows it)",
		"id", t.ID, "name", t.Name, "scope", string(t.Scope), "file", path)
	return secret, true, nil
}

// mcpTokenPropagation bounds how long `itervox mcp` waits for a running
// daemon to accept a token it just created: the daemon re-reads
// tokens.json at most every 200 ms.
const mcpTokenPropagation = 3 * time.Second

// waitForMCPToken waits until the daemon accepts a new token (GET
// /api/v1/whoami stops answering 401). A daemon that still refuses it is
// only logged: the first tool call reports the problem.
func waitForMCPToken(ctx context.Context, c *mcpserver.Client) {
	deadline := time.Now().Add(mcpTokenPropagation)
	for {
		err := c.Do(ctx, http.MethodGet, "/whoami", nil, nil, nil)
		var apiErr *mcpserver.APIError
		if err == nil || !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
			return
		}
		if time.Now().After(deadline) {
			slog.Warn("mcp: the daemon does not accept the new mcp token yet", "error", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
