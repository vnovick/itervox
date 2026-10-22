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
  every one takes --workflow, --logs-dir and --url):
  watch   a read-only, plain-text view of running, retrying, queued,
          input-required and paused issues, refreshed every --interval
          (default 2s); --once prints it once (works over SSH, no TTY)
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

  --version  Print version information

Run mode (default when no command given; headless unless --tui):
`)
	flag.PrintDefaults()
}
