package config

import (
	"log/slog"
	"slices"
	"strings"
)

// DefaultGuardrailApprovalCommands is guardrails.approval_commands when the
// key is absent (#204): deploy and publish commands an agent may only run
// after a human approves them. Each entry is a sequence of glob tokens: the
// first matches the program name, the rest must appear among its arguments
// in order (other arguments may sit between them).
var DefaultGuardrailApprovalCommands = []string{
	"vercel --prod",
	"fly deploy",
	"kubectl apply",
	"terraform apply",
	"gcloud deploy",
	"npm publish",
	"cargo publish",
}

// GuardrailsConfig is the guardrails: block (#204): limits on what an agent
// started by Itervox may do with git, the GitHub/GitLab CLIs and deploy
// commands. Applied to dispatched local runs and to tmux sessions started
// with "New session"; never to hand-started sessions. Guidance, not a
// sandbox: an agent that calls git by another path bypasses it. Read-only
// after startup.
type GuardrailsConfig struct {
	// Enabled turns the guardrails on (default true).
	Enabled bool
	// AllowForcePush lets an agent force-push and delete its own issue
	// branch. Pushes to other branches and tag pushes stay refused.
	// Default false.
	AllowForcePush bool
	// ApprovalCommands are the command patterns that need a human approval
	// before they run. Absent: DefaultGuardrailApprovalCommands. A list
	// replaces the defaults, so write them out to keep them; an empty list
	// turns approvals off.
	ApprovalCommands []string
}

// parseGuardrailsConfig reads the guardrails: block. A pattern whose program
// token is a path or a glob cannot be intercepted on PATH; it is kept (it can
// still match a shimmed program's arguments) but warned about.
func parseGuardrailsConfig(m map[string]any) GuardrailsConfig {
	c := GuardrailsConfig{
		Enabled:        boolField(m, "enabled", true),
		AllowForcePush: boolField(m, "allow_force_push", false),
	}
	raw := strSliceField(m, "approval_commands", nil)
	if v, set := m["approval_commands"]; !set || v == nil {
		raw = DefaultGuardrailApprovalCommands
	}
	for _, p := range raw {
		p = strings.Join(strings.Fields(p), " ")
		if p == "" || slices.Contains(c.ApprovalCommands, p) {
			continue
		}
		if program, _, _ := strings.Cut(p, " "); strings.ContainsAny(program, "/*?[") {
			slog.Warn("config: guardrails.approval_commands entry names its program by path or glob; only a bare program name can be intercepted",
				"pattern", p)
		}
		c.ApprovalCommands = append(c.ApprovalCommands, p)
	}
	return c
}
