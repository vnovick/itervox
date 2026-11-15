package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/hub"
)

// `itervox token` (#214) manages the stored API tokens in
// .itervox/tokens.json: named viewer / operator / admin tokens, kept as
// SHA-256 hashes only. It writes the file directly, so it works whether or
// not the daemon runs; a running daemon notices the change within a second
// (mtime check) without a restart. A new secret is printed once, alone on
// stdout (details go to stderr), and never logged.
//
//	itervox token create --name NAME --scope viewer|operator|admin [--expires 90d]
//	itervox token list
//	itervox token revoke ID
//	itervox token rotate [ID]   (no ID: the only stored admin token)
//
// --hub (#228) points every subcommand at the hub's own store instead,
// ~/.itervox/hub/tokens.json: hub tokens open the hub URL and cap what the
// hub may do on every member.

const tokenUsage = `usage: itervox token create --name NAME --scope viewer|operator|admin [--expires 90d|12h|2027-01-31] [--workflow WORKFLOW.md | --hub]
       itervox token list [--workflow WORKFLOW.md | --hub] [--json]
       itervox token revoke ID [--workflow WORKFLOW.md | --hub]
       itervox token rotate [ID] [--workflow WORKFLOW.md | --hub]   (no ID: the only stored admin token)
--hub manages the hub's own token store (~/.itervox/hub/tokens.json), whose tokens open the hub URL.`

// tokenCLIActor names the CLI in the store's audit log lines.
const tokenCLIActor = "cli"

func runToken(args []string) {
	fatalExit(tokenCmd(args, os.Stdout, os.Stderr, time.Now()))
}

// tokenCmd runs `itervox token` and returns the exit code.
func tokenCmd(args []string, out, errOut io.Writer, now time.Time) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(errOut, tokenUsage)
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("token "+sub, flag.ContinueOnError)
	fs.SetOutput(errOut)
	workflow := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md (the store is .itervox/tokens.json next to it)")
	name := fs.String("name", "", "create: the token's name")
	scopeFlag := fs.String("scope", "", "create: viewer, operator or admin")
	expiresFlag := fs.String("expires", "", "create: expiry, e.g. 90d, 12h, 2027-01-31 (default: never)")
	asJSON := fs.Bool("json", false, "list: print JSON")
	hubStore := fs.Bool("hub", false, "manage the hub's token store (~/.itervox/hub/tokens.json, #228) instead of the project's")
	// Flags may come before or after the positional ID.
	var positional []string
	rest := args[1:]
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintf(errOut, "itervox token %s: %v\n", sub, err)
		return 1
	}
	storePath := apitokens.Path(*workflow)
	if *hubStore {
		dir, err := hubStateDir()
		if err == nil {
			err = ensurePrivateDir(dir)
		}
		if err != nil {
			return fail(err)
		}
		storePath = filepath.Join(dir, hub.TokenStoreFileName)
	}
	store := apitokens.Open(storePath)
	switch sub {
	case "create":
		if len(positional) > 0 {
			_, _ = fmt.Fprintln(errOut, tokenUsage)
			return 2
		}
		scope, err := apitokens.ParseScope(*scopeFlag)
		if err != nil {
			return fail(err)
		}
		expires, err := apitokens.ParseExpiry(*expiresFlag, now)
		if err != nil {
			return fail(err)
		}
		t, secret, err := store.Create(*name, scope, expires, tokenCLIActor)
		if err != nil {
			return fail(err)
		}
		printNewSecret(out, errOut, "created", t, secret, store.FilePath())
		return 0
	case "list":
		if len(positional) > 0 {
			_, _ = fmt.Fprintln(errOut, tokenUsage)
			return 2
		}
		tokens, err := store.List()
		if err != nil {
			return fail(err)
		}
		return printTokenList(out, tokens, *asJSON, now)
	case "revoke":
		if len(positional) != 1 {
			_, _ = fmt.Fprintln(errOut, "itervox token revoke: name exactly one token ID (see itervox token list)")
			return 2
		}
		t, err := store.Revoke(positional[0], tokenCLIActor)
		if err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintf(out, "revoked %s (%s, %s); a running daemon refuses it within a second\n", t.ID, t.Name, t.Scope)
		return 0
	case "rotate":
		if len(positional) > 1 {
			_, _ = fmt.Fprintln(errOut, tokenUsage)
			return 2
		}
		var id string
		if len(positional) == 1 {
			id = positional[0]
		} else {
			sole, err := soleAdminTokenID(store)
			if err != nil {
				return fail(err)
			}
			id = sole
		}
		t, secret, err := store.Rotate(id, tokenCLIActor)
		if err != nil {
			return fail(err)
		}
		printNewSecret(out, errOut, "rotated", t, secret, store.FilePath())
		return 0
	}
	_, _ = fmt.Fprintln(errOut, tokenUsage)
	return 2
}

// soleAdminTokenID is `itervox token rotate` without an ID: the one stored
// admin token. The bootstrap token (ITERVOX_API_TOKEN, the auto-generated or
// supervisor token) is not in the store; it changes when the daemon restarts
// with a new value.
func soleAdminTokenID(store *apitokens.Store) (string, error) {
	tokens, err := store.List()
	if err != nil {
		return "", err
	}
	var ids []string
	for _, t := range tokens {
		if t.Scope == apitokens.ScopeAdmin {
			ids = append(ids, t.ID)
		}
	}
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		return "", errors.New("no stored admin token to rotate (create one with: itervox token create --name admin --scope admin); " +
			"the ITERVOX_API_TOKEN / auto-generated token is not stored and changes when the daemon restarts with a new value")
	}
	return "", fmt.Errorf("%d stored admin tokens (%s); name the one to rotate", len(ids), strings.Join(ids, ", "))
}

// printNewSecret prints the secret alone on stdout and the details on
// stderr, so `itervox token create ... > file` captures just the secret.
func printNewSecret(out, errOut io.Writer, verb string, t apitokens.Token, secret, path string) {
	_, _ = fmt.Fprintf(errOut, "%s %s token %s (%s) in %s\n", verb, t.Scope, t.ID, t.Name, path)
	if t.Expires != nil {
		_, _ = fmt.Fprintf(errOut, "expires %s\n", t.Expires.Format(time.RFC3339))
	}
	_, _ = fmt.Fprintln(errOut, "the token below is shown once; store it now (it is kept only as a hash):")
	_, _ = fmt.Fprintln(out, secret)
}

func printTokenList(out io.Writer, tokens []apitokens.Token, asJSON bool, now time.Time) int {
	type row struct {
		ID       string     `json:"id"`
		Name     string     `json:"name"`
		Scope    string     `json:"scope"`
		Created  time.Time  `json:"created"`
		LastUsed *time.Time `json:"lastUsed,omitempty"`
		Expires  *time.Time `json:"expires,omitempty"`
		Expired  bool       `json:"expired,omitempty"`
	}
	rows := make([]row, len(tokens))
	for i, t := range tokens {
		rows[i] = row{t.ID, t.Name, string(t.Scope), t.Created, t.LastUsed, t.Expires, t.Expired(now)}
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"tokens": rows})
		return 0
	}
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(out, "no stored tokens (the ITERVOX_API_TOKEN / auto-generated token is always admin and not listed)")
		return 0
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tSCOPE\tCREATED\tLAST USED\tEXPIRES")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Scope,
			r.Created.Format(time.DateOnly), timeOrDash(r.LastUsed, time.DateTime), expiryLabel(r.Expires, r.Expired))
	}
	_ = tw.Flush()
	return 0
}

func timeOrDash(t *time.Time, layout string) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format(layout)
}

func expiryLabel(t *time.Time, expired bool) string {
	if t == nil {
		return "never"
	}
	if expired {
		return "expired " + t.Format(time.DateOnly)
	}
	return t.Format(time.DateOnly)
}

// openTokenStore opens the project's stored-token index for the server and
// starts the lastUsed flusher, which writes pending lastUsed times every
// tokenFlushInterval and once more when ctx ends (the generation stops).
func openTokenStore(ctx context.Context, workflowPath string) *apitokens.Store {
	store := apitokens.Open(apitokens.Path(workflowPath))
	go func() {
		defer failFastOnPanic("token-lastused-flusher")
		ticker := time.NewTicker(tokenFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				flushTokenLastUsed(store)
				return
			case <-ticker.C:
				flushTokenLastUsed(store)
			}
		}
	}()
	return store
}

// tokenFlushInterval is how often the daemon persists token lastUsed times.
const tokenFlushInterval = 30 * time.Second

func flushTokenLastUsed(store *apitokens.Store) {
	if err := store.FlushLastUsed(); err != nil {
		slog.Warn("apitokens: could not record token last use", "path", store.FilePath(), "error", err)
	}
}
