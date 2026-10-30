package main

import (
	"flag"
	"fmt"
	"os"
)

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: itervox [command] [flags]

Commands:
  demo    Try Itervox in about a minute: a throwaway board with fake
          issues and a scripted agent (no tracker, keys or agent CLI).
             --dir      scratch directory (default: a new temp dir)
             --no-open  do not open the dashboard in a browser
             --tracker  memory|local  (default: memory)

  quickstart  From a repository to a running board in one command:
          detect the tracker and agent CLI, write (or migrate) WORKFLOW.md,
          set up the tracker token and GitHub labels (asks first), run
          doctor, start the daemon in the background and print the URL.
             --tracker  github|linear|local  (default: detected)
             --runner   claude|codex   (default: detected)
             --dir      repository directory (default: .)
             --workflow workflow path (default: <dir>/WORKFLOW.md)
             --yes, -y  answer yes to every confirmation
             --no-start run the checks only; do not start the daemon
             --ready-timeout  how long to wait for the daemon (default: 90s)

  init    Scan a repository and generate a WORKFLOW.md starter file,
          or migrate an existing one with --update.
             --tracker  linear|github|local  (required for new workflows)
             --runner   claude|codex    (default: claude)
             --output   output file path (default: WORKFLOW.md)
             --dir      directory to scan (default: .)
             --force    overwrite existing output file
             --issue-template  add the agent-ready issue template
                        without asking (GitHub; printed for Linear)
             --update   migrate an existing WORKFLOW.md to the latest
                        schema (writes WORKFLOW.md.bak, extracts inline
                        profile prompts to .itervox/agents/<name>/
                        INSTRUCTIONS.md, seeds SOUL.md, stamps
                        itervox_schema_version)
             --workflow path to the WORKFLOW.md to migrate when using
                        --update (default: WORKFLOW.md)

  run     Run one issue to completion and exit (CI): agent turns,
          reviewers, push, PR, issue comment, tracker move. No server.
             --issue    the issue (required)
             --workflow path to WORKFLOW.md (default: WORKFLOW.md)
             --worktree work in a worktree, not the checkout itself
             --answer   TEXT|@FILE: answer the question of a run that
                        exited 3, and continue from its handoff
             --approve  CMD: allow this guardrail approval command for this run
             --timeout  cap the whole run (e.g. 45m)
             --json     print one JSON object instead of Markdown
             --log-dir  write redacted logs here
          Exit codes: 0 PR opened or nothing to do, 2 failed,
          3 needs input, 4 budget exceeded, 5 configuration error.

  clear   Remove workspace directories created by itervox
             --workflow path to WORKFLOW.md (default: WORKFLOW.md)
             [identifier ...]  specific issues to clear; omit for all

  stop    Stop all daemons serving the current project (uses PID file +
          process scan as fallback, so pre-upgrade daemons are caught)
             --workflow path to WORKFLOW.md (default: WORKFLOW.md)
             --grace    SIGTERM → SIGKILL grace period (default: 30s)
             --force    skip the grace period and SIGKILL immediately

  status  List running itervox daemons for the current project
             --workflow path to WORKFLOW.md (default: WORKFLOW.md)
             --all      also list daemons from other projects

  secret  Set or list .itervox/.env values without showing them
             set KEY    read the value with echo off (or from stdin)
             list       show each key as set / placeholder / empty
             --workflow path to WORKFLOW.md (default: WORKFLOW.md)
             --replace  (set) replace a value that is already set
             --hub      the hub's own ~/.itervox/hub/.env (Slack tokens)

  token   Manage stored API tokens (.itervox/tokens.json, hashes only):
          viewer, operator or admin scope. A running daemon picks up a
          change within a second; no restart. New tokens print once.
             create --name NAME --scope viewer|operator|admin [--expires 90d]
             list   [--json]
             revoke ID
             rotate [ID]  (no ID: the only stored admin token)
             --workflow path to WORKFLOW.md (default: WORKFLOW.md)
             --hub      the hub's own token store (~/.itervox/hub/tokens.json)

  hub     Print the machine's hub URL and its member projects (--json).
          The first daemon started becomes the hub: one URL for every
          project (hub.port, default 8089, loopback), opened with a hub
          token (itervox token create --hub). --no-hub opts a daemon out.
             slack setup   print the Slack app manifest, store the app and
                           bot tokens (echo off) and pick a channel per
                           project (--json: JSON manifest; --manifest-only)
             slack status  tokens set or missing, hub.slack settings and
                           each project's channel (--json)

  mcp     Serve the board and memory of this project's running daemon to
          an MCP client over stdio (claude mcp add itervox -- itervox mcp).
          Uses a stored "mcp" token created on first use (viewer; remembered
          in .itervox/mcp-token.json), or --token / ITERVOX_TOKEN
             --token-scope operator  also offer dispatch, pause, replies,
                        steering and remember
             --project  the project directory (default: the --workflow dir)
             --url      the daemon's address (default: discovered)

  config  Show the effective settings and the layer each came from
          (default, ~/.itervox/settings.jsonc, .itervox/settings.jsonc,
          .itervox/settings.local.jsonc, WORKFLOW.md), or edit a
          settings file with its comments kept
             show       every value with its layer (--json for JSON)
             set KEY VALUE  set agent.*, polling.*, server.* or
                        workspace.* in .itervox/settings.jsonc (--user:
                        ~/.itervox/settings.jsonc)
             unset KEY  remove a key from that file
             --local    (set, unset) the gitignored
                        .itervox/settings.local.jsonc instead
             schema     print the settings JSON Schema
             --workflow path to WORKFLOW.md (default: WORKFLOW.md)

  import  Convert a Symphony or Itervox v0.x WORKFLOW.md: settings move
          to .itervox/settings.jsonc, profile prompts to prompt files;
          writes WORKFLOW.md.bak and prints every key it could not map
             --force    overwrite an existing WORKFLOW.md.bak

  export  --symphony: write a Symphony WORKFLOW.md (core keys and the
          worker prompt) and list the Itervox-only features it dropped
             --workflow path to WORKFLOW.md (default: WORKFLOW.md)
             --out      output path, or - for stdout
                        (default: WORKFLOW.symphony.md)
             --force    overwrite --out

  report  costs: tokens and estimated cost per issue and per day
             --days N   days to list (default 14)  --json  JSON output

  Talking to a running daemon (it finds the daemon from .itervox/dashboard_url
  and its token from ITERVOX_API_TOKEN, .itervox/.env or <logs-dir>/api-token;
  every one takes --workflow, --logs-dir and --url, and --project NAME to reach
  one hub project with the hub token in ITERVOX_HUB_TOKEN):
  watch   a read-only live panel: status header, one lane per running issue
          with its latest (redacted) activity, feed events as they happen,
          and the exact reply/approve command for every agent waiting on you.
          Never reads stdin or changes the terminal mode (SSH, tmux, no TTY).
             --issue ID      follow one issue's full log and its events
             --events-only   the event stream alone (for piping)
             --json          one JSON object per line
             --since SEQ     replay the feed events after cursor SEQ
             --interval 2s   refresh interval; --once prints once and exits
  logs    print an issue's run log; -f keeps following new lines
             itervox logs [-f] <issue>
  pause    <issue>         stop the issue's agent and pause the issue
  resume   <issue>         resume a paused issue
  pause    [--reason r] [--for 2h]
                           pause ALL dispatch: running agents continue,
                           nothing new starts (until resume, or --for)
  resume                   resume all dispatch
  dispatch <issue>         move the issue to the first active state
  workers  <n>             set max_concurrent_agents (saved to WORKFLOW.md)
  reply    <issue> <text>  answer an agent's question
  approve  <issue>         approve the command a guardrail is holding (once)

  --version  Print version information

Run mode (default when no command given; always headless — watch it with
itervox watch or the dashboard):
`)
	flag.PrintDefaults()
}
