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
	// kind is the token's apitokens Kind ("" or apitokens.KindHub); a
	// remembered token of another kind is replaced, one with no kind is
	// re-stamped.
	kind string
}

// ensureStoredToken returns the secret of the token spec describes, of
// scope, creating one through the project's token store and remembering it
// in .itervox/<spec.file> (mode 0600) when there is no usable one. A
// remembered token that was revoked, expired, rotated or has another scope
// or kind is replaced, the old one revoked when it is still stored; one
// whose kind is only missing (dropped by an older member's rewrite) gets
// its kind back (apitokens.Store.SetKind) and keeps its secret. The
// secret is never printed or logged.
func ensureStoredToken(workflowPath string, spec storedTokenSpec, scope apitokens.Scope) (secret string, created bool, err error) {
	store := apitokens.Open(apitokens.Path(workflowPath))
	path := storedTokenPath(workflowPath, spec.file)
	old, err := readRememberedToken(path)
	if err != nil {
		return "", false, err
	}
	tokens, err := store.List()
	if err != nil {
		return "", false, fmt.Errorf("read the token store: %w", err)
	}
	stored := slices.IndexFunc(tokens, func(t apitokens.Token) bool { return old.ID != "" && t.ID == old.ID })
	if stored >= 0 {
		t := tokens[stored]
		usable := rememberedTokenUsable(old, t, scope)
		if usable && t.Kind == spec.kind {
			return old.Secret, false, nil
		}
		// Our own token (its secret is the one remembered) with its kind
		// missing: a member on an older binary rewrote tokens.json (every
		// lastUsed flush, create or revoke) and dropped the field it does
		// not know. Re-stamp the kind and keep the secret: replacing the
		// token would revoke the old one and close every open terminal it
		// carries, again after every such rewrite. Hub attribution (the
		// caller header a member trusts only on a KindHub token) needs the
		// member on a binary that keeps the field.
		if usable && t.Kind == "" && spec.kind != "" {
			_, err := store.SetKind(t.ID, spec.kind, spec.actor)
			switch {
			case err == nil:
				return old.Secret, false, nil
			case !errors.Is(err, apitokens.ErrNotFound):
				// The secret still works; only attribution waits for the
				// next attempt.
				slog.Warn(spec.label+": could not restore the stored token's kind", "id", t.ID, "error", err)
				return old.Secret, false, nil
			}
			stored = -1 // revoked meanwhile: create a new one, nothing to revoke
		}
	}
	var t apitokens.Token
	if spec.kind != "" {
		t, secret, err = store.CreateKind(spec.name, scope, spec.kind, spec.actor)
	} else {
		t, secret, err = store.Create(spec.name, scope, nil, spec.actor)
	}
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

// readRememberedToken reads a storedTokenSpec file; a missing or
// unparsable file is the zero value (no remembered token).
func readRememberedToken(path string) (mcpTokenFile, error) {
	var old mcpTokenFile
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if json.Unmarshal(raw, &old) != nil {
			return mcpTokenFile{}, nil
		}
		return old, nil
	case errors.Is(err, fs.ErrNotExist):
		return mcpTokenFile{}, nil
	default:
		return mcpTokenFile{}, fmt.Errorf("read %s: %w", path, err)
	}
}

// rememberedTokenUsable reports whether the stored token t is still the
// remembered one (its secret hashes to t's hash) and is of scope, unexpired.
func rememberedTokenUsable(old mcpTokenFile, t apitokens.Token, scope apitokens.Scope) bool {
	return old.Scope == scope && t.Scope == scope && !t.Expired(time.Now()) &&
		t.Hash == apitokens.HashSecret(old.Secret)
}

// restampStoredToken re-stamps spec.kind on the remembered token of scope
// when a member on an older binary dropped it (an old-format rewrite of
// tokens.json), without waiting for a look-up: the hub calls it for every
// member token it holds cached, on each member refresh. It only ever calls
// apitokens.Store.SetKind: it never creates, replaces or revokes a token
// and keeps the secret, so the cached secret stays valid and no open
// terminal closes. A token that is gone, rotated, expired, of another
// scope or kind is left alone (the next look-up after the member's 401
// replaces it). restamped reports a re-stamp.
func restampStoredToken(workflowPath string, spec storedTokenSpec, scope apitokens.Scope) (restamped bool, err error) {
	if spec.kind == "" {
		return false, nil
	}
	old, err := readRememberedToken(storedTokenPath(workflowPath, spec.file))
	if err != nil || old.ID == "" {
		return false, err
	}
	store := apitokens.Open(apitokens.Path(workflowPath))
	tokens, err := store.List()
	if err != nil {
		return false, fmt.Errorf("read the token store: %w", err)
	}
	i := slices.IndexFunc(tokens, func(t apitokens.Token) bool { return t.ID == old.ID })
	if i < 0 || tokens[i].Kind != "" || !rememberedTokenUsable(old, tokens[i], scope) {
		return false, nil
	}
	if _, err := store.SetKind(old.ID, spec.kind, spec.actor); err != nil {
		if errors.Is(err, apitokens.ErrNotFound) {
			return false, nil // revoked meanwhile: the next look-up replaces it
		}
		return false, err
	}
	return true, nil
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
