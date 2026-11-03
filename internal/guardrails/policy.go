// Package guardrails keeps agents that Itervox starts away from a few
// dangerous operations (#204): pushes to anything but the issue branch,
// force pushes, branch deletions and tag pushes (a pre-push hook installed
// through core.hooksPath); merging their own pull request (PATH shims for
// git, gh and glab); and deploy commands, which wait for a human approval
// (PATH shims for the programs in guardrails.approval_commands).
//
// A run's guardrails live in a private runtime directory (Prepare): a
// policy file, a bin/ directory of shims put first on the agent's PATH, and
// a hooks/ directory git uses through GIT_CONFIG_PARAMETERS. Shims and the
// pre-push hook are small sh scripts that run the itervox binary itself
// (Main), which decides and then either refuses, asks the daemon for an
// approval, or execs the real program with the original arguments and
// environment. Every other hook name chains to the repository's own hooks
// directory, so the repository's hooks still run.
//
// Known limit: this is guidance, not a sandbox. An agent that runs git by an
// absolute path, rewrites its PATH or environment, or reaches the remote
// another way is not stopped. Remote branch protection is what makes a
// protected-branch push impossible.
//
// Itervox's own git commands never see any of this: they go through
// internal/gitexec, whose scrubbed environment drops GIT_CONFIG_PARAMETERS.
// The shim's forward is an exec of the agent's own command, not a git
// command Itervox runs; the git the pre-push hook needs (ancestry checks)
// goes through gitexec like every other.
package guardrails

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// Block kinds: the bounded label set of itervox_guardrail_blocks_total and
// the Source of a guardrail RecentFailures entry.
const (
	// KindBranch: a push to a ref other than the issue branch.
	KindBranch = "branch"
	// KindForcePush: a push that rewrites the issue branch's history.
	KindForcePush = "force_push"
	// KindDelete: a push that deletes a branch.
	KindDelete = "delete"
	// KindTag: a tag push.
	KindTag = "tag"
	// KindHookBypass: an attempt to skip or replace the guardrail hook
	// (git push --no-verify, git -c core.hooksPath=…).
	KindHookBypass = "hook_bypass"
	// KindSelfMerge: gh pr merge, glab mr merge, a merge API call, or a
	// git merge into the default branch.
	KindSelfMerge = "self_merge"
	// KindApprovalRejected: a human rejected a command that needed approval.
	KindApprovalRejected = "approval_rejected"
	// KindApprovalUnavailable: a command needed approval and no approval
	// channel was available (tmux sessions, no daemon endpoint, the run
	// ended while it waited).
	KindApprovalUnavailable = "approval_unavailable"
)

// Kinds lists every block kind (for metrics pre-initialisation and for
// rejecting a report with an unknown kind).
var Kinds = []string{KindBranch, KindForcePush, KindDelete, KindTag, KindHookBypass,
	KindSelfMerge, KindApprovalRejected, KindApprovalUnavailable}

// IsKind reports whether kind is one of Kinds.
func IsKind(kind string) bool { return slices.Contains(Kinds, kind) }

// TokenAction is the action name a guardrail token is granted in the
// per-run action-grant store (internal/agentactions). It grants only the
// guardrail report and approval endpoints.
const TokenAction = "guardrail"

// Policy is the runtime policy file a run's shims and hooks read.
type Policy struct {
	// Identifier is the issue identifier (dispatched runs) or the tmux
	// session ID ("New session").
	Identifier string `json:"identifier"`
	// IssueBranch is the branch the agent may push to.
	IssueBranch string `json:"issue_branch"`
	// ProtectedBranches can never be pushed to or merged into.
	ProtectedBranches []string `json:"protected_branches"`
	// AllowForcePush is guardrails.allow_force_push.
	AllowForcePush bool `json:"allow_force_push"`
	// ApprovalCommands is guardrails.approval_commands.
	ApprovalCommands []string `json:"approval_commands"`
	// ApprovalsEnabled is false where no human can answer an approval
	// (tmux sessions, no daemon endpoint): matching commands are refused.
	ApprovalsEnabled bool `json:"approvals_enabled"`
	// OrigHooksDir is the repository's own hooks directory, chained after
	// the guardrail checks. Empty when the directory was not a repository.
	OrigHooksDir string `json:"orig_hooks_dir"`
	// Dir is the runtime directory; BinDir its shims.
	Dir    string `json:"dir"`
	BinDir string `json:"bin_dir"`
	// DaemonURL and Token reach the daemon's guardrail endpoints (reports
	// and approvals). Empty when no endpoint is available.
	DaemonURL string `json:"daemon_url,omitempty"`
	Token     string `json:"token,omitempty"`
}

// policyFileName and adoptedFileName live in the runtime directory.
const (
	policyFileName  = "policy.json"
	adoptedFileName = "adopted-branch"
)

// LoadPolicy reads a policy file.
func LoadPolicy(path string) (Policy, error) {
	var p Policy
	data, err := os.ReadFile(path)
	if err != nil {
		return p, fmt.Errorf("guardrails: read policy: %w", err)
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("guardrails: parse policy: %w", err)
	}
	if p.Dir == "" {
		return p, errors.New("guardrails: policy has no runtime directory")
	}
	return p, nil
}

func writePolicy(p Policy) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("guardrails: encode policy: %w", err)
	}
	if err := atomicfs.WriteFile(filepath.Join(p.Dir, policyFileName), data, 0o600); err != nil {
		return fmt.Errorf("guardrails: write policy: %w", err)
	}
	return nil
}

// ProtectedBranches are the branches no agent pushes to or merges into:
// main, master, develop and the configured base branches.
func ProtectedBranches(base ...string) []string {
	out := []string{"main", "master", "develop"}
	for _, b := range base {
		if b = strings.TrimSpace(b); b != "" && !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	return out
}

// protected reports whether branch may never be pushed to or merged into.
func (p Policy) protected(branch string) bool {
	return branch == "" || branch == "HEAD" || slices.Contains(p.ProtectedBranches, branch)
}

// adoptedBranch is the branch this run adopted on its first push of a new
// branch (see CheckPush); "" when none.
func (p Policy) adoptedBranch() string {
	data, err := os.ReadFile(filepath.Join(p.Dir, adoptedFileName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (p Policy) adopt(branch string) error {
	return atomicfs.WriteFile(filepath.Join(p.Dir, adoptedFileName), []byte(branch+"\n"), 0o600)
}

// allowedBranches are the branches a push may update.
func (p Policy) allowedBranches() []string {
	var out []string
	for _, b := range []string{p.IssueBranch, p.adoptedBranch()} {
		if b != "" && !p.protected(b) && !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	return out
}
