package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// approveCommand answers identifier's guardrail approval request (#204)
// with "approve": the held command runs once. It refuses when the issue is
// not waiting for an approval, so a question is never answered "approve".
func approveCommand(ctx context.Context, c *daemonClient, identifier string, out io.Writer) error {
	var st watchState
	if err := c.request(ctx, http.MethodGet, "/state", nil, &st); err != nil {
		return err
	}
	found := false
	for _, in := range st.InputRequired {
		if in.Identifier == identifier && in.waiting() {
			if !in.GuardrailApproval {
				return fmt.Errorf("%s is waiting for an answer, not an approval: answer it with itervox reply %s \"…\"", identifier, shellWord(identifier))
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%s has no approval request waiting", identifier)
	}
	if err := c.request(ctx, http.MethodPost, "/issues/"+url.PathEscape(identifier)+"/provide-input", map[string]string{"message": "approve"}, nil); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Approved for %s: the held command runs once.\n", identifier)
	return nil
}
