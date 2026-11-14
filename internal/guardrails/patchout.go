package guardrails

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/agent"
)

// Patches only (push: daemon). The agent of such a profile emits commits
// and nothing else: its environment carries no forge or tracker
// credentials (AgentEnv), the shims refuse every push and every gh/glab
// command that opens, merges or changes a pull request or writes through
// the API, and the pre-push hook refuses whatever push still reaches it.
// The daemon pushes the branch itself, after the run's gates passed and
// the lanes allow it (internal/orchestrator, patch_out.go), with its own
// credentials and through internal/gitexec, which never sees these shims
// or the hook.
//
// Known limit, as for every guardrail: without process isolation an agent
// can still read a credential file of the daemon's user by path, or reach
// the forge another way; with isolation: process the files in
// agent.IsolationPatchOutHomeDeny (and the isolation's own deny list) are
// unreadable.

// PatchOutMessage is what every patch-out refusal tells the agent.
const PatchOutMessage = "this profile emits patches; the daemon pushes after its checks"

// patchOutRefusal refuses what (a push or a pull-request command).
func patchOutRefusal(what string) decision {
	return decision{act: actRefuse, kind: KindPatchOut, message: "guardrails: `" + what + "` is refused: " +
		PatchOutMessage + ". Commit your work on the issue branch and end the run; Itervox pushes the branch " +
		"and opens the pull request once its checks pass."}
}

// ghPatchOutWrites and glabPatchOutWrites are the `<noun> <verb>` pairs a
// patch-out agent may not run: anything that publishes or changes a pull
// or merge request, a repository or a release.
var (
	ghPatchOutWrites = map[string][]string{
		"pr":      {"create", "merge", "edit", "ready", "close", "reopen", "comment", "review", "lock", "unlock"},
		"repo":    {"create", "fork", "delete", "edit", "sync", "rename", "archive", "unarchive", "deploy-key"},
		"release": {"create", "upload", "delete", "delete-asset", "edit"},
	}
	glabPatchOutWrites = map[string][]string{
		"mr":      {"create", "new", "merge", "accept", "update", "close", "reopen", "note", "approve", "rebase", "delete"},
		"repo":    {"create", "new", "fork", "delete", "archive", "transfer"},
		"release": {"create", "new", "upload", "delete"},
	}
)

// decidePatchOutCLI refuses tool's write commands (writes) and write API
// calls. Positional words are looked at like decideMergeCLI does: the noun
// may follow one global option value (`gh -R owner/repo pr create`).
func decidePatchOutCLI(tool string, args []string, writes map[string][]string) decision {
	var pos []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
		}
	}
	for i := 0; i+1 < len(pos) && i < 2; i++ {
		if verbs, ok := writes[pos[i]]; ok && slices.Contains(verbs, pos[i+1]) {
			return patchOutRefusal(tool + " " + pos[i] + " " + pos[i+1])
		}
	}
	if slices.Contains(pos[:min(len(pos), 2)], "api") && isWriteAPI(args) {
		return patchOutRefusal(tool + " api")
	}
	return decision{}
}

// isWriteAPI reports whether `gh api` / `glab api` args write: an explicit
// method other than GET, request fields or an input body without an
// explicit GET (both CLIs then default to POST), or a GraphQL mutation.
func isWriteAPI(args []string) bool {
	method := ""
	fields := false
	for i, a := range args {
		name, value, hasValue := strings.Cut(a, "=")
		switch {
		case a == "-X" || a == "--method":
			if i+1 < len(args) {
				method = args[i+1]
			}
		case name == "--method" && hasValue:
			method = value
		case strings.HasPrefix(a, "-X") && len(a) > 2:
			method = a[2:]
		case a == "-f" || a == "-F" || a == "--field" || a == "--raw-field" || a == "--input",
			(name == "--field" || name == "--raw-field" || name == "--input") && hasValue,
			(strings.HasPrefix(a, "-f") || strings.HasPrefix(a, "-F")) && len(a) > 2 && !strings.HasPrefix(a, "--"):
			fields = true
		}
		if strings.Contains(a, "mutation") {
			return true
		}
	}
	if method != "" {
		return !strings.EqualFold(method, "GET")
	}
	return fields
}

// ForgeCredentialEnv are the variables a patch-out agent's environment
// never carries: forge and tracker tokens, and what git and ssh would ask
// for a credential with. Itervox's own action token (ITERVOX_ACTION_TOKEN,
// scoped to the run's allowed actions) is not one of them.
var ForgeCredentialEnv = []string{
	"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
	"GITLAB_TOKEN", "GL_TOKEN", "GITLAB_PRIVATE_TOKEN", "CI_JOB_TOKEN",
	"GITEA_TOKEN", "BITBUCKET_TOKEN", "LINEAR_API_KEY",
	"GIT_ASKPASS", "SSH_ASKPASS", "SSH_AUTH_SOCK",
}

// forgeConfigDir is the runtime subdirectory gh and glab get as their
// configuration directory: empty, so they find no stored login.
const forgeConfigDir = "forge"

// patchOutGitConfig is the git configuration a patch-out agent's git sees
// before core.hooksPath: an empty credential.helper, which drops every
// helper the user's configuration lists, so git has no credential to push
// with.
func patchOutGitConfig() string {
	return gitConfigParam("credential.helper", "") + " "
}

// patchOutEnv adds push: daemon's environment to env: no credential
// helper, no prompts, gh and glab pointed at an empty configuration
// directory, and ForgeCredentialEnv (plus the run's own extra names, such
// as the variables holding the tracker key) removed.
func (r *Runtime) patchOutEnv(env agent.RunEnv) agent.RunEnv {
	cfgDir := filepath.Join(r.policy.Dir, forgeConfigDir)
	env.Vars = append(env.Vars,
		"GIT_TERMINAL_PROMPT=0",
		"GH_PROMPT_DISABLED=1",
		"GH_CONFIG_DIR="+filepath.Join(cfgDir, "gh"),
		"GLAB_CONFIG_DIR="+filepath.Join(cfgDir, "glab"),
	)
	unset := slices.Clone(ForgeCredentialEnv)
	for _, name := range r.stripEnv {
		if name != "" && !slices.Contains(unset, name) {
			unset = append(unset, name)
		}
	}
	env.Unset = unset
	return env
}

// writePatchOutDirs creates the empty gh and glab configuration
// directories.
func (r *Runtime) writePatchOutDirs() error {
	for _, d := range []string{"gh", "glab"} {
		if err := os.MkdirAll(filepath.Join(r.policy.Dir, forgeConfigDir, d), 0o700); err != nil {
			return err
		}
	}
	return nil
}

// EnvNamesWithValue returns the names of the variables in environ (os.Environ
// form) whose value is value: how the daemon finds every variable that holds
// its tracker key, whatever it is called. Nothing for an empty value.
func EnvNamesWithValue(environ []string, value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var out []string
	for _, kv := range environ {
		if name, v, ok := strings.Cut(kv, "="); ok && v == value && name != "" {
			out = append(out, name)
		}
	}
	return out
}

// ClassifyPush classifies the daemon's own push (push: daemon) of commit
// sha to refs/heads/branch from the repository in dir exactly as the
// pre-push hook classifies an agent's push: remoteSHA is the branch's tip
// on the remote, "" for a new branch. Every path is auto when lanes is
// empty; changes that cannot be listed are human (fail closed).
func ClassifyPush(dir, branch, sha, remoteSHA string, lanes Lanes, baseRefs []string) LaneResult {
	if lanes.Empty() {
		return LaneResult{Lane: LaneAuto}
	}
	return Policy{Lanes: lanes, BaseRefs: baseRefs}.classifyPush([]RefUpdate{daemonRefUpdate(branch, sha, remoteSHA)}, gitRepo{dir: dir})
}

// DaemonPushKey identifies a held daemon push for its approval request:
// the same branch and commit join the pending request, a new commit asks
// again.
func DaemonPushKey(dir, branch, sha string) string {
	return laneKey(dir, []RefUpdate{daemonRefUpdate(branch, sha, "")})
}

func daemonRefUpdate(branch, sha, remoteSHA string) RefUpdate {
	if remoteSHA == "" {
		remoteSHA = strings.Repeat("0", 40)
	}
	ref := "refs/heads/" + branch
	return RefUpdate{LocalRef: ref, LocalSHA: sha, RemoteRef: ref, RemoteSHA: remoteSHA}
}
