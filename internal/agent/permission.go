package agent

import "log/slog"

// PermissionMode selects the approval/sandbox flags an agent turn is launched
// with (issue #66).
//
// Itervox runs agents non-interactively, so a mode that can pause for human
// approval is not merely slower — it hangs the turn until the timeout kills it
// and the issue fails. Every mode here is therefore non-interactive; they
// differ in how much the agent is allowed to do without asking, not in whether
// it can ask.
type PermissionMode string

const (
	// PermissionBypass is the long-standing behaviour and remains the default:
	// approvals are bypassed entirely. It is what makes a headless run work at
	// all, and what a PreToolUse hook in the target repo constrains — a deny
	// hook, unlike a deny rule, is NOT bypassed by these flags.
	PermissionBypass PermissionMode = "bypass"

	// PermissionSandbox keeps the run non-interactive but confines it.
	//
	// Codex maps cleanly: --sandbox workspace-write --ask-for-approval never.
	//
	// Claude is the weaker half, deliberately. --permission-mode acceptEdits
	// auto-accepts edits without prompting, but Claude has no equivalent of a
	// filesystem sandbox, so this bounds what the agent is asked to confirm,
	// not what the process can reach. Operators wanting real containment on
	// the Claude side should run the daemon itself in a container; the guide
	// says so rather than letting the mode name imply more than it delivers.
	PermissionSandbox PermissionMode = "sandbox"
)

// DefaultPermissionMode is what an unset profile field resolves to. Changing
// it would silently alter how every existing deployment launches agents, so it
// is pinned to the pre-#66 behaviour.
const DefaultPermissionMode = PermissionBypass

// ParsePermissionMode resolves a configured string, falling back to the
// default with a warning rather than failing startup.
//
// Fail-soft is the right call here: a typo in a permission mode should not
// stop a daemon from running the work it already has queued, and the fallback
// is the behaviour the operator had before they edited the field. A hard
// failure would be defensible if the fallback were MORE permissive than the
// requested mode — it is not; bypass is what they were already running.
func ParsePermissionMode(s string) PermissionMode {
	switch PermissionMode(s) {
	case PermissionBypass, PermissionSandbox:
		return PermissionMode(s)
	case "":
		return DefaultPermissionMode
	default:
		slog.Warn("agent: unknown permission_mode, falling back to the default",
			"requested", s, "using", string(DefaultPermissionMode))
		return DefaultPermissionMode
	}
}

// claudePermissionFlags returns the approval flags for a claude invocation.
func claudePermissionFlags(mode PermissionMode) []string {
	if mode == PermissionSandbox {
		return []string{"--permission-mode", "acceptEdits"}
	}
	return []string{"--dangerously-skip-permissions"}
}

// codexPermissionFlags returns the approval flags for a codex invocation.
//
// Both forms are non-interactive. --ask-for-approval never is what keeps the
// sandbox mode from turning into a hang: without it, a workspace-write sandbox
// still prompts on the first action outside the workspace.
func codexPermissionFlags(mode PermissionMode) []string {
	if mode == PermissionSandbox {
		return []string{"--sandbox", "workspace-write", "--ask-for-approval", "never"}
	}
	return []string{"--dangerously-bypass-approvals-and-sandbox"}
}
