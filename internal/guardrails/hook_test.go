package guardrails

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hookPolicyForTest() Policy {
	return Policy{
		ProtectedBranches: ProtectedBranches(),
		ApprovalCommands:  []string{"vercel --prod"},
		EnvApprovalCommands: []EnvApproval{
			{Match: "terraform apply", WhenEnv: map[string]string{"TF_WORKSPACE": "prod*"}},
			{Match: "kubectl apply", WhenEnv: map[string]string{"KUBE_STAGE": "!dev*"}},
		},
		Lanes: Lanes{Human: []string{".github/**", "Makefile"}},
	}
}

func bash(p Policy, cmd string, env ...string) HookVerdict {
	branchOf := func(string) string { return "feature" }
	return p.hookCommandLine(cmd, "/work", LookupIn(env), branchOf, 0)
}

// The hook denies what the shims and pre-push hook refuse, wherever the
// command sits in the line, and lets everything else through.
func TestHookDeniesGuardrailCommand(t *testing.T) {
	p := hookPolicyForTest()
	for _, cmd := range []string{
		"gh pr merge 12 --squash",
		"cd sub && gh pr merge 12",
		"npm test; glab mr merge 7",
		"/usr/local/bin/gh pr merge 1",
		"sudo -u bot gh pr merge 1",
		"env FOO=1 gh pr merge 1",
		`bash -lc "gh pr merge 3"`,
		"echo $(gh pr merge 4)",
		"echo `gh pr merge 5`",
		`eval "gh pr merge 6"`,
		"gh api -X PUT repos/acme/app/pulls/12/merge",
		"git push --no-verify",
		"git -c core.hooksPath=/dev/null push",
		"git push origin main",
		"git push origin HEAD:refs/heads/master",
		"git push -f origin feature",
		"git push origin +feature",
		"git push origin :feature",
		"git push --tags",
		"git push origin refs/tags/v1",
		"git push --all",
	} {
		v := bash(p, cmd)
		assert.Equal(t, HookDeny, v.Decision, cmd)
		assert.Contains(t, v.Reason, "guardrails:", cmd)
	}
	for _, cmd := range []string{
		"gh pr view 12",
		"gh pr create --title merge",
		"git push origin feature",
		"git push",
		`echo "gh pr merge 1"`,
		"cat <<EOF\ngh pr merge 1\nEOF\necho done",
		"grep -r 'git push origin main' docs/ 2>/dev/null | head",
		"vercel deploy",
		"# gh pr merge 1",
	} {
		assert.Equal(t, HookVerdict{}, bash(p, cmd), cmd)
	}
	// A bare push from a protected branch pushes that branch.
	v := p.hookCommandLine("git push", "/work", LookupIn(nil), func(string) string { return "main" }, 0)
	assert.Equal(t, HookDeny, v.Decision)
	// allow_force_push lets a force push of a non-protected branch through.
	p.AllowForcePush = true
	assert.Equal(t, HookVerdict{}, bash(p, "git push --force-with-lease origin feature"))
	assert.Equal(t, HookDeny, bash(p, "git push -f origin main").Decision)
}

// approval_commands ask, and an env-scoped rule asks only when the
// environment the command would run with matches: the hook's environment,
// overridden by the line's own assignments; a value only known at run
// time counts as matching.
func TestHookAsksForApprovalCommands(t *testing.T) {
	p := hookPolicyForTest()
	asks := []struct {
		cmd string
		env []string
	}{
		{"vercel deploy --prod", nil},
		{"TF_WORKSPACE=prod-eu terraform apply", nil},
		{"terraform apply -auto-approve", []string{"TF_WORKSPACE=prod"}},
		{"export TF_WORKSPACE=prod; terraform apply", nil},
		{"TF_WORKSPACE=prod; terraform apply", nil},
		{"env TF_WORKSPACE=prod terraform apply", nil},
		{"TF_WORKSPACE=$(cat ws) terraform apply", nil},
		{"kubectl apply -f k8s/", nil}, // KUBE_STAGE unset: !dev* holds
		{"KUBE_STAGE=prod kubectl -n x apply -f k8s/", nil},
		{"$TOOL apply", nil},
	}
	for _, tc := range asks {
		v := bash(p, tc.cmd, tc.env...)
		assert.Equal(t, HookAsk, v.Decision, tc.cmd)
	}
	assert.Contains(t, bash(p, "vercel --prod").Reason, `(guardrails.approval_commands: "vercel --prod")`)
	assert.Contains(t, bash(p, "terraform apply", "TF_WORKSPACE=prod").Reason, `"terraform apply" when TF_WORKSPACE=prod*`)
	for _, tc := range []struct {
		cmd string
		env []string
	}{
		{"terraform apply", nil},
		{"TF_WORKSPACE=dev terraform apply", []string{"TF_WORKSPACE=prod"}},
		{"terraform apply", []string{"TF_WORKSPACE=staging"}},
		{"KUBE_STAGE=dev-1 kubectl apply -f k8s/", nil},
		{"terraform plan", []string{"TF_WORKSPACE=prod"}},
	} {
		assert.Equal(t, HookVerdict{}, bash(p, tc.cmd, tc.env...), tc.cmd)
	}
	// Deny outranks ask on one line.
	assert.Equal(t, HookDeny, bash(p, "vercel --prod && gh pr merge 1").Decision)
}

// An edit of a guardrails.lanes.human file asks, with the pipeline-file
// reason; other files, and files outside the repository, are not held.
func TestHookAsksForHumanLaneEdit(t *testing.T) {
	r := newRig(t, nil)
	p := hookPolicyForTest()
	edit := func(tool, path string) HookVerdict {
		in := HookInput{ToolName: tool, Cwd: r.work}
		in.ToolInput.FilePath = path
		return p.EvaluateHook(in, nil, r.work)
	}
	for _, tc := range []struct{ tool, path string }{
		{"Edit", filepath.Join(r.work, ".github", "workflows", "ci.yml")},
		{"Write", filepath.Join(r.work, "Makefile")},
		{"MultiEdit", "sub/Makefile"}, // relative: taken from cwd
	} {
		v := edit(tc.tool, tc.path)
		assert.Equal(t, HookAsk, v.Decision, tc.path)
		assert.Contains(t, v.Reason, HumanLaneEditReason, tc.path)
	}
	assert.Contains(t, edit("Edit", filepath.Join(r.work, ".github/workflows/ci.yml")).Reason, ".github/workflows/ci.yml")
	assert.Equal(t, HookVerdict{}, edit("Edit", filepath.Join(r.work, "main.go")))
	assert.Equal(t, HookVerdict{}, edit("Write", filepath.Join(t.TempDir(), "Makefile")), "outside the repository")
	assert.Equal(t, HookVerdict{}, edit("Read", filepath.Join(r.work, "Makefile")), "not an edit")

	// Codex apply_patch: the patch's file headers are the edited paths.
	in := HookInput{ToolName: "apply_patch", Cwd: r.work}
	in.ToolInput.Command = "*** Begin Patch\n*** Update File: main.go\n@@\n-a\n+b\n*** Add File: .github/workflows/x.yml\n+on: push\n*** End Patch\n"
	v := p.EvaluateHook(in, nil, r.work)
	assert.Equal(t, HookAsk, v.Decision)
	assert.Contains(t, v.Reason, ".github/workflows/x.yml")
}

// The hook output is the documented PreToolUse JSON; nothing for allow;
// for Codex, which does not support ask, an ask becomes a deny.
func TestHookOutputShape(t *testing.T) {
	assert.Nil(t, HookOutput(HookAgentClaude, HookVerdict{}))
	var got struct {
		HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal(HookOutput(HookAgentClaude, HookVerdict{Decision: HookAsk, Reason: "r"}), &got))
	assert.Equal(t, map[string]string{"hookEventName": "PreToolUse", "permissionDecision": "ask", "permissionDecisionReason": "r"}, got.HookSpecificOutput)
	require.NoError(t, json.Unmarshal(HookOutput(HookAgentCodex, HookVerdict{Decision: HookAsk, Reason: "r"}), &got))
	assert.Equal(t, "deny", got.HookSpecificOutput["permissionDecision"])
	assert.Contains(t, got.HookSpecificOutput["permissionDecisionReason"], "cannot ask")
}

// An env-scoped approval command is held by the shim only when the
// command's real environment (at exec time, after expansion) matches.
func TestEnvScopedApprovalAppliesOnlyInMatchingEnv(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.EnvApprovalCommands = []EnvApproval{{Match: "terraform apply", WhenEnv: map[string]string{"TF_WORKSPACE": "prod*"}}}
	})
	fakes := filepath.Join(r.root, "fakebin")
	ran := filepath.Join(r.root, "ran")
	fakeProgram(t, fakes, "terraform", "echo \"$TF_WORKSPACE $*\" >> "+ran+"\n")
	out, code := r.agent("TF_WORKSPACE=prod-eu terraform apply -auto-approve", fakes)
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, `needs a human approval (guardrails.approval_commands: "terraform apply" when TF_WORKSPACE=prod*)`)
	ws := "prod"
	out, code = r.agent(`W=`+ws+`; export TF_WORKSPACE="$W"; terraform apply`, fakes) // only known after expansion
	assert.Equal(t, 1, code, out)
	_, err := os.Stat(ran)
	assert.True(t, os.IsNotExist(err), "terraform never ran against prod")

	out, code = r.agent("TF_WORKSPACE=dev terraform apply -auto-approve", fakes)
	assert.Zero(t, code, out)
	out, code = r.agent("terraform apply", fakes) // unset: not prod
	assert.Zero(t, code, out)
	data, err := os.ReadFile(ran)
	require.NoError(t, err)
	assert.Equal(t, "dev apply -auto-approve\n apply\n", string(data))
	assert.Contains(t, ShimNames(r.rt.policy.approvalPatterns()), "terraform", "the scoped rule's program is shimmed")
}

// A plain approval command behaves exactly as before env-scoped rules:
// held whatever the environment, with the same message and shims.
func TestPlainApprovalCommandsUnchanged(t *testing.T) {
	r := newRig(t, nil) // ApprovalCommands: vercel --prod, approvals unavailable
	fakes := filepath.Join(r.root, "fakebin")
	fakeProgram(t, fakes, "vercel", "exit 0\n")
	for _, env := range []string{"", "TF_WORKSPACE=dev ", "TF_WORKSPACE=prod "} {
		out, code := r.agent(env+"vercel --prod", fakes)
		assert.Equal(t, 1, code, out)
		assert.Contains(t, out, "guardrails: `vercel --prod` needs a human approval (guardrails.approval_commands: \"vercel --prod\"), and approvals are not available here.")
	}
	assert.Equal(t, []string{"gh", "glab", "git", "vercel"}, ShimNames(r.rt.policy.approvalPatterns()))
	why, ok := Policy{ApprovalCommands: []string{"kubectl apply"}}.matchAnyApproval("kubectl", []string{"apply"}, LookupIn(nil))
	assert.True(t, ok)
	assert.Equal(t, `"kubectl apply"`, why)
}

func TestParseShellLine(t *testing.T) {
	words := func(line string) [][]string {
		var out [][]string
		for _, c := range parseShellLine(line).Commands {
			var ws []string
			for _, w := range c.Words {
				ws = append(ws, w.Text)
			}
			out = append(out, ws)
		}
		return out
	}
	assert.Equal(t, [][]string{{"a", "b c", "d"}, {"e"}, {"f"}, {"g", "x"}}, words(`a 'b c' "d" && e || f | g x 2>&1 >/dev/null`))
	assert.Equal(t, [][]string{{"cat"}, {"echo", "ok"}}, words("cat <<-'EOF'\n\tgh pr merge\n\tEOF\necho ok"))
	assert.Equal(t, [][]string{{"echo", "a\"b", "c d"}}, words(`echo a\"b c\ d`))
	p := parseShellLine(`x=$(gh pr merge 1) y "$(git push)"`)
	assert.Equal(t, []string{"gh pr merge 1", "git push"}, p.Nested)
	assert.True(t, p.Commands[0].Words[0].Dynamic)
}
