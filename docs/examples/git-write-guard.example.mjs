#!/usr/bin/env node
// EXAMPLE PreToolUse(Bash) hook — copy into your own repo's .claude/hooks/
// and register it in .claude/settings.json.
//
//   interactive session -> deny git commit / git push
//   itervox agent run   -> allow, minus force-pushes and pushes to main/master
//
// itervox sets ITERVOX_AGENT=1 on every agent turn. SECURITY: that is a
// convenience marker, NOT authentication — any process can set it. Branch
// protection on your remote is what actually makes a forced or
// protected-branch push impossible. This hook is a guardrail, not a sandbox:
// an interpreter one-liner or a script file can still reach git.
//
// The regexes below are deliberately simple string matches, not a shell
// parser, and will miss exotic spellings that still resolve to a git write,
// e.g.:
//   git -C /repo push            (path flag before the subcommand)
//   X=git; $X push               (command built from a variable)
//   bash -c "git push"           (git invoked from inside a nested shell)
// Treat this as an example to adapt to your own repo's conventions, not a
// hardened guard against a determined adversary.
import { readFileSync } from "node:fs";

const deny = (reason) => {
  process.stdout.write(JSON.stringify({
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: reason,
    },
  }));
  process.exit(0);
};

let cmd = "";
try {
  cmd = JSON.parse(readFileSync(0, "utf8"))?.tool_input?.command ?? "";
} catch {
  process.exit(0);
}

if (!/\bgit\b/.test(cmd)) process.exit(0);
const isCommit = /\bgit\b[^|;&]*\bcommit\b/.test(cmd);
const isPush = /\bgit\b[^|;&]*\bpush\b/.test(cmd);
if (!isCommit && !isPush) process.exit(0);

if (isPush && /--force|--force-with-lease|\s-[A-Za-z]*f\b|\s\+/.test(cmd)) {
  deny("force-push rewrites published history and is never automatic. Report the divergence instead.");
}
if (isPush && /\b(main|master)\b\s*$/.test(cmd)) {
  deny("pushing straight to main/master bypasses review. Push a feature branch and open a PR.");
}

if (process.env.ITERVOX_AGENT === "1") process.exit(0); // allow the itervox run

deny("git commit/push is denied in interactive sessions. Leave the work staged and run it yourself.");
