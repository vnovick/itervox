package evals

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/agent"
)

// The eval sandbox (#189) puts two shims first on the live agent's PATH:
//
//   - `itervox` (also $ITERVOX_BIN) answers `itervox action ...` like the
//     daemon-backed action CLI would ("ok") and records the call;
//   - `gh` answers the read commands a profile uses to inspect a PR
//     (`pr list`, `pr view`, `pr checks`, `pr diff`) from the scenario's
//     input.yaml, records every write (`pr review`, `pr comment`,
//     `pr merge`, `api`, ...) and refuses anything it cannot fake.
//
// Every call is appended to calls.log; after the run, sandboxEvents turns
// the log into recording events: daemon actions become
// {"event":"action","action":"<snake_case>"}, and any comment-shaped body
// (`itervox action comment --body`, `gh pr review --body`, ...) also becomes
// {"event":"comment","body":...} — the shape the judges read.
//
// The log is one record per call: fields separated by \037, records
// terminated by \036. A `--body-file`/`-F`/`--findings` argument carries the
// file's content after a \035, captured when the shim runs (agents often
// delete their temp files).
const (
	fieldSep   = "\x1f"
	recordSep  = "\x1e"
	contentSep = "\x1d"
)

// sandbox is one scenario run's throwaway directory tree.
type sandbox struct {
	Root      string
	Workspace string
	ShimDir   string
	CallsLog  string
}

func newSandbox(root string, in ScenarioInput) (*sandbox, error) {
	sb := &sandbox{
		Root:      root,
		Workspace: filepath.Join(root, "workspace"),
		ShimDir:   filepath.Join(root, "bin"),
		CallsLog:  filepath.Join(root, "calls.log"),
	}
	responses := filepath.Join(root, "responses")
	for _, dir := range []string{sb.Workspace, sb.ShimDir, responses} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(sb.CallsLog, nil, 0o644); err != nil {
		return nil, err
	}
	if err := writeGHResponses(responses, in.pullRequests()); err != nil {
		return nil, err
	}
	shims := map[string]string{
		"gh":      ghShim(sb.CallsLog, responses),
		"itervox": itervoxShim(sb.CallsLog),
	}
	for name, script := range shims {
		if err := os.WriteFile(filepath.Join(sb.ShimDir, name), []byte(script), 0o755); err != nil {
			return nil, err
		}
	}
	return sb, nil
}

// env is the environment the agent command runs with: shims first on PATH,
// ITERVOX_BIN at the itervox shim, and the action variables the real action
// CLI requires (pointing nowhere — the shim never calls the daemon).
func (sb *sandbox) env(identifier string) map[string]string {
	path := sb.ShimDir
	if cur := os.Getenv("PATH"); cur != "" {
		path += string(os.PathListSeparator) + cur
	}
	return map[string]string{
		"PATH":                     path,
		"ITERVOX_BIN":              filepath.Join(sb.ShimDir, "itervox"),
		"ITERVOX_ISSUE_IDENTIFIER": identifier,
		"ITERVOX_DAEMON_URL":       "http://itervox-eval.invalid",
		"ITERVOX_ACTION_TOKEN":     "itervox-eval-sandbox",
		"ITERVOX_EVAL_SANDBOX":     "1",
		// Tracker credentials are blanked so an agent that reaches past the
		// shims (an absolute gh path, curl) cannot act on a real tracker.
		"GH_TOKEN":       "",
		"GITHUB_TOKEN":   "",
		"LINEAR_API_KEY": "",
	}
}

// logPreamble is the shared shim prologue: append argv (and any body-file
// content) to the calls log as one record.
func logPreamble(tool, callsLog string) string {
	return "#!/bin/sh\n" +
		"# itervox eval sandbox shim (#189). Generated per run; never installed.\n" +
		"LOG=" + agent.ShellQuote(callsLog) + "\n" +
		"{\n" +
		"  printf '%s' " + tool + "\n" +
		"  prev=\n" +
		"  for a in \"$@\"; do\n" +
		"    printf '\\037%s' \"$a\"\n" +
		"    case \"$prev\" in\n" +
		"      --body-file|-F|--findings)\n" +
		"        if [ \"$a\" = - ]; then printf '\\035'; cat; elif [ -f \"$a\" ]; then printf '\\035'; cat \"$a\"; fi ;;\n" +
		"    esac\n" +
		"    prev=$a\n" +
		"  done\n" +
		"  printf '\\036'\n" +
		"} >> \"$LOG\"\n"
}

func itervoxShim(callsLog string) string {
	return logPreamble("itervox", callsLog) +
		"case \"$1 $2\" in\n" +
		"  'action comment'|'action comment-pr'|'action create-issue'|'action move-state'|'action provide-input'|'action merge-pr') echo ok ;;\n" +
		"  *) echo \"itervox: only 'itervox action ...' is available in the eval sandbox\" >&2; exit 2 ;;\n" +
		"esac\n"
}

func ghShim(callsLog, responses string) string {
	return logPreamble("gh", callsLog) +
		"DIR=" + agent.ShellQuote(responses) + "\n" +
		"num=\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in\n" +
		"    */pull/[0-9]*) [ -z \"$num\" ] && num=${a##*/pull/} ;;\n" +
		"    ''|*[!0-9]*) ;;\n" +
		"    *) [ -z \"$num\" ] && num=$a ;;\n" +
		"  esac\n" +
		"done\n" +
		"[ -n \"$num\" ] || num=default\n" +
		"case \"$1 $2\" in\n" +
		"  'pr list') cat \"$DIR/pr-list.json\" ;;\n" +
		"  'pr view'|'pr checks'|'pr diff')\n" +
		"    f=\"$DIR/$2-$num.out\"\n" +
		"    if [ ! -f \"$f\" ]; then echo \"no pull requests found\" >&2; exit 1; fi\n" +
		"    cat \"$f\"; exit \"$(cat \"$DIR/$2-$num.code\" 2>/dev/null || echo 0)\" ;;\n" +
		"  'pr review'|'pr comment'|'pr merge'|'pr edit'|'pr close'|'issue comment'|'issue edit')\n" +
		"    echo \"https://github.com/itervox-eval/sandbox/pull/$num\" ;;\n" +
		"  'auth status') echo 'Logged in to github.com (itervox eval sandbox)' ;;\n" +
		"  *) echo \"gh $1 $2: not available in the itervox eval sandbox\" >&2; exit 1 ;;\n" +
		"esac\n"
}

// writeGHResponses lays down the canned `gh` answers for the scenario's
// PRs. `pr view`/`pr checks`/`pr diff` without a number answer for the
// first PR (the issue branch's PR, as gh resolves it from the checkout).
func writeGHResponses(dir string, prs []sandboxPR) error {
	type label struct {
		Name string `json:"name"`
	}
	type prJSON struct {
		Number           int     `json:"number"`
		HeadRefName      string  `json:"headRefName"`
		URL              string  `json:"url"`
		State            string  `json:"state"`
		Labels           []label `json:"labels"`
		Mergeable        string  `json:"mergeable"`
		MergeStateStatus string  `json:"mergeStateStatus"`
		Reviews          []any   `json:"reviews"`
	}
	list := make([]prJSON, 0, len(prs))
	files := map[string]string{}
	for i, pr := range prs {
		labels := make([]label, 0, len(pr.Labels))
		for _, l := range pr.Labels {
			labels = append(labels, label{Name: l})
		}
		state, checksCode, checks := "CLEAN", "0", ""
		for _, c := range pr.Checks {
			status := strings.ToLower(c.Status)
			bucket := "pass"
			switch status {
			case "failing", "fail", "failure":
				bucket, state, checksCode = "fail", "BLOCKED", "1"
			case "pending":
				bucket = "pending"
				if checksCode == "0" {
					state, checksCode = "BLOCKED", "8"
				}
			}
			checks += fmt.Sprintf("%s\t%s\t1m0s\thttps://github.com/itervox-eval/sandbox/actions/runs/%d\n", c.Name, bucket, pr.Number)
		}
		entry := prJSON{
			Number: pr.Number, HeadRefName: pr.Head, State: "OPEN", Labels: labels,
			URL:       "https://github.com/itervox-eval/sandbox/pull/" + strconv.Itoa(pr.Number),
			Mergeable: "MERGEABLE", MergeStateStatus: state, Reviews: []any{},
		}
		list = append(list, entry)
		view, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			return err
		}
		keys := []string{strconv.Itoa(pr.Number)}
		if i == 0 {
			keys = append(keys, "default")
		}
		for _, key := range keys {
			files["view-"+key+".out"] = string(view) + "\n"
			files["checks-"+key+".out"] = checks
			files["checks-"+key+".code"] = checksCode
			files["diff-"+key+".out"] = pr.Diff
		}
	}
	listJSON, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	files["pr-list.json"] = string(listJSON) + "\n"
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// RecordedEvent is one recording.jsonl line.
type RecordedEvent struct {
	Event  string         `json:"event"`
	Action string         `json:"action,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	Body   string         `json:"body,omitempty"`
}

// sandboxCall is one parsed shim invocation.
type sandboxCall struct {
	Tool string
	Args []string
	// Files maps an argument index to the content of the file it named.
	Files map[int]string
}

func parseCallsLog(data string) []sandboxCall {
	var calls []sandboxCall
	for _, rec := range strings.Split(data, recordSep) {
		if rec == "" {
			continue
		}
		fields := strings.Split(rec, fieldSep)
		call := sandboxCall{Tool: fields[0], Files: map[int]string{}}
		for i, f := range fields[1:] {
			arg, content, hasFile := strings.Cut(f, contentSep)
			call.Args = append(call.Args, arg)
			if hasFile {
				call.Files[i] = content
			}
		}
		calls = append(calls, call)
	}
	return calls
}

// flagValue returns the value of the first of names in args, accepting
// `--name value`, `--name=value` and Go's single-dash forms; a file-valued
// flag returns the captured file content.
func (c sandboxCall) flagValue(names ...string) (string, bool) {
	for i, a := range c.Args {
		for _, n := range names {
			if a == n && i+1 < len(c.Args) {
				if content, ok := c.Files[i+1]; ok {
					return content, true
				}
				return c.Args[i+1], true
			}
			if v, ok := strings.CutPrefix(a, n+"="); ok {
				return v, true
			}
		}
	}
	return "", false
}

func (c sandboxCall) has(flag string) bool {
	for _, a := range c.Args {
		if a == flag {
			return true
		}
	}
	return false
}

// ghReadOnly are the gh subcommands that only read; they leave no event.
var ghReadOnly = map[string]bool{"list": true, "view": true, "checks": true, "diff": true, "status": true}

// sandboxEvents converts the calls log into recording events, in call order.
func sandboxEvents(calls []sandboxCall) []RecordedEvent {
	var out []RecordedEvent
	for _, c := range calls {
		switch c.Tool {
		case "itervox":
			if len(c.Args) < 2 || c.Args[0] != "action" {
				continue
			}
			name := strings.ReplaceAll(c.Args[1], "-", "_")
			args := map[string]any{}
			for _, f := range []string{"state", "pr", "title", "strategy", "message"} {
				if v, ok := c.flagValue("--"+f, "-"+f); ok {
					args[f] = v
				}
			}
			out = append(out, RecordedEvent{Event: "action", Action: name, Args: args})
			switch name {
			case "comment":
				if body, ok := c.flagValue("--body", "-body"); ok {
					out = append(out, RecordedEvent{Event: "comment", Body: body})
				}
			case "comment_pr":
				summary, _ := c.flagValue("--summary", "-summary")
				findings, _ := c.flagValue("--findings", "-findings")
				if body := strings.TrimSpace(summary + "\n" + findings); body != "" {
					out = append(out, RecordedEvent{Event: "comment", Body: body})
				}
			}
		case "gh":
			if len(c.Args) == 0 {
				continue
			}
			name := "gh_" + c.Args[0]
			if len(c.Args) > 1 && !strings.HasPrefix(c.Args[1], "-") {
				if ghReadOnly[c.Args[1]] {
					continue
				}
				name += "_" + c.Args[1]
			}
			args := map[string]any{}
			for _, f := range []string{"--approve", "--comment", "--request-changes"} {
				if c.has(f) {
					args[strings.TrimPrefix(f, "--")] = true
				}
			}
			out = append(out, RecordedEvent{Event: "action", Action: name, Args: args})
			if body, ok := c.flagValue("--body", "-b", "--body-file", "-F"); ok {
				out = append(out, RecordedEvent{Event: "comment", Body: body})
			}
		}
	}
	return out
}
