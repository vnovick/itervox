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

// mcpTokenFile is the shape of .itervox/mcp-token.json (and of every
// storedTokenSpec file).
type mcpTokenFile struct {
	ID     string          `json:"id"`
	Scope  apitokens.Scope `json:"scope"`
	Secret string          `json:"secret"`
}

// storedTokenPath is .itervox/<file> next to workflowPath.
func storedTokenPath(workflowPath, file string) string {
	return filepath.Join(filepath.Dir(workflowPath), ".itervox", file)
}

// ensureMCPToken returns the secret of the project's mcp token of scope,
// creating (and remembering) one when there is no usable one; created
// reports a new token.
func ensureMCPToken(workflowPath string, scope apitokens.Scope) (secret string, created bool, err error) {
	return ensureStoredToken(workflowPath, storedTokenSpec{
		file: mcpTokenFileName, name: mcpTokenName, actor: mcpTokenActor, label: "mcp",
	}, scope)
}

// storedTokenSpec names a stored token a local client of the daemon keeps
// for itself in the project's token store: `itervox mcp` (#216) and the
// hub's per-member tokens (#228) share ensureStoredToken.
type storedTokenSpec struct {
	file  string // the remembered secret's file inside .itervox (0600, gitignored)
	name  string // the token's name in the store
	actor string // the audit actor of create / revoke
	label string // the client, in log lines
}

// ensureStoredToken returns the secret of the token spec describes, of
// scope, creating one through the project's token store and remembering it
// in .itervox/<spec.file> (mode 0600) when there is no usable one. A
// remembered token that was revoked, expired, rotated or has another scope
// is replaced, the old one revoked when it is still stored. The secret is
// never printed or logged.
func ensureStoredToken(workflowPath string, spec storedTokenSpec, scope apitokens.Scope) (secret string, created bool, err error) {
	store := apitokens.Open(apitokens.Path(workflowPath))
	path := storedTokenPath(workflowPath, spec.file)
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
	t, secret, err := store.Create(spec.name, scope, nil, spec.actor)
	if err != nil {
		return "", false, fmt.Errorf("create the %s token: %w", spec.label, err)
	}
	if err := ensureItervoxGitignore(filepath.Dir(path)); err != nil {
		slog.Warn(spec.label+": could not make sure the remembered token is gitignored", "file", path, "error", err)
	}
	data, _ := json.MarshalIndent(mcpTokenFile{ID: t.ID, Scope: scope, Secret: secret}, "", "  ")
	if err := atomicfs.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		_, _ = store.Revoke(t.ID, spec.actor) // never leave an unreachable token behind
		return "", false, fmt.Errorf("remember the %s token in %s: %w", spec.label, path, err)
	}
	if stored >= 0 && tokens[stored].Name == spec.name {
		if _, err := store.Revoke(old.ID, spec.actor); err != nil {
			slog.Warn(spec.label+": could not revoke the replaced token", "id", old.ID, "error", err)
		}
	}
	slog.Info(spec.label+": created a stored token (itervox token list shows it)",
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
