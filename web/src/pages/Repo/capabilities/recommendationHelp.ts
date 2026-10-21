// Per-recommendation optimization help for the skills analyzer's issue IDs
// (Repo Analysis → Capabilities and Runtime usage, #200), shown when a
// recommendation row is expanded.

export interface RecommendationHelp {
  what: string;
  howToFix: string[];
}

export const RECOMMENDATION_HELP: Partial<Record<string, RecommendationHelp>> = {
  HIGH_COST_LOW_USAGE: {
    what: 'A skill carries significant context cost (≥ 2 000 tokens) but the daemon has not seen it loaded in the recent session window. Either it is genuinely unused, or it is only triggered by tasks you have not run lately.',
    howToFix: [
      'Confirm the skill is still relevant: search recent work for the skill name.',
      'If unused: delete the SKILL.md folder, or move it from your global ~/.claude/skills/ to a project that actually invokes it.',
      'If used but not detected: ensure your sessions write to ~/.itervox/logs/ (set CLAUDE_CODE_LOG_DIR=~/.itervox/logs/<repo>).',
      'Re-scan after 25 sessions of normal use to refresh the runtime view.',
    ],
  },
  CONFIGURED_NOT_LOADED: {
    what: 'The skill is in your filesystem inventory but no session in the last 25 has loaded it. Possible causes: dead config, recently-installed but not exercised yet, or the daemon has no session logs to inspect.',
    howToFix: [
      'Run a few normal sessions and re-scan — recent installs need a few invocations to show up.',
      'If genuinely unused: delete the SKILL.md folder.',
      'If your skill should auto-trigger: review its `description:` frontmatter for trigger keywords; weak descriptions never fire.',
    ],
  },
  LOADED_NOT_CONFIGURED: {
    what: 'A capability appears in runtime evidence but is missing from the static filesystem inventory. Likely an ambient install (a global Claude Code plugin) or a stale scan cache.',
    howToFix: [
      'Click Re-scan to refresh the inventory.',
      'If still missing: check ~/.claude/plugins/ and ~/.codex/plugins/ for plugin layouts the scanner does not cover.',
      'If your install is non-standard, file the path so the scanner can be extended.',
    ],
  },
  HOOK_STORM: {
    what: 'A hook fired ≥ 50 times in the lookback window. Each invocation expands hook context into the next prompt — frequent firing inflates per-call cost AND chips at model attention.',
    howToFix: [
      'Tighten the hook matcher: pin to specific tools (e.g. `Bash`, `Edit`) instead of wildcarding `*`.',
      'Move logic out of the hook into a one-shot script the user invokes when needed.',
      'Search for duplicate hooks (same event + similar command) and consolidate.',
    ],
  },
  DUPLICATE_SKILL: {
    what: 'The same skill name exists in multiple scopes. Closest scope wins (project > user > system); the others are dead weight in your token budget every time the skill description gets loaded.',
    howToFix: [
      'Decide which scope should own this skill — usually project-level for repo-specific, user-level for personal, system for shared org.',
      'Delete the duplicates from the other scopes.',
      'If both copies must coexist (different content), rename one — e.g. `linear-workflow` → `linear-workflow-personal`.',
    ],
  },
  DUPLICATE_MCP: {
    what: 'The same MCP server command or URL is registered in multiple settings files (project + user + .mcp.json). Both run; both contribute tool schemas to context — usually harmless but doubles overhead.',
    howToFix: [
      'Pick the right scope (project for repo-specific, user for global).',
      'Manually remove the duplicate from the other settings.json — the daemon does NOT touch user settings automatically (intentional safety).',
      'After editing, click Re-scan.',
    ],
  },
  UNUSED_PROFILE: {
    what: 'An agent profile is defined in WORKFLOW.md but no run in the recent-history sample used it. Either it is a leftover from a past experiment or it gets dispatched only by edge-case automations.',
    howToFix: [
      'Click "Disable profile" to set `enabled: false` in WORKFLOW.md (non-destructive — the file persists and you can re-enable later).',
      'If you want to remove it entirely, edit WORKFLOW.md::agent.profiles directly.',
      'Verify automations do not reference it (they will error at dispatch time if they do).',
    ],
  },
  BLOATED_PROFILE: {
    what: 'Your inventory carries a heavy capability surface (more than 20 MCP servers OR more than 15 skills). Every profile inherits this surface, so each agent invocation loads the full tool catalog into context.',
    howToFix: [
      'Audit which skills the agent actually uses by running for a week with CLAUDE_CODE_LOG_DIR set, then re-scan and look at the runtime analytics.',
      'Delete or narrow-trigger the skills that never load.',
      'Move project-specific skills out of your global ~/.claude/skills/ into the project repo.',
      'Consider splitting into multiple profiles, each with a tighter scope (e.g. a reviewer profile with only review-relevant skills).',
    ],
  },
  LARGE_CONTEXT: {
    what: 'A profile estimated context cost exceeds 50 000 tokens. Beyond ~50 K you start paying noticeable per-call cost AND degrading model attention.',
    howToFix: [
      'Check the Context Budget bars to find the dominant category (skills / MCP / instructions).',
      'For instructions: split a very long CLAUDE.md into per-directory CLAUDE.md files (loaded only when those dirs are touched).',
      'For skills: see BLOATED_PROFILE optimizations.',
      'For MCP: drop servers whose tools never get called in your runtime evidence.',
    ],
  },
  STALE_SCHEDULE: {
    what: 'A schedule-inventory entry references a profile name that no longer exists in your config. v0.2.0 does not populate schedules from validated WORKFLOW.md automations, so this can only appear from future or custom inventory sources.',
    howToFix: [
      'Edit the schedule definition to reference an existing profile.',
      'Or delete the schedule if it is no longer needed.',
    ],
  },
  INSTRUCTION_SHADOWING: {
    what: 'The same instruction filename (e.g. CLAUDE.md) appears in multiple scopes. Project-level wins for behavior — the others contribute zero behavior but their text still loads into context if the path is reachable.',
    howToFix: [
      'Decide which scope is authoritative.',
      'Trim or delete the redundant copies.',
      'If you genuinely need different content per scope, that is fine — just verify the override is intentional.',
    ],
  },
  MISSING_SKILL_REF: {
    what: "A profile's SOUL.md or INSTRUCTIONS.md names a skill (`name` skill) that is not installed for that profile's backend, so the agent runs without it.",
    howToFix: [
      'Check the name for a typo against the Skills list above.',
      'If the skill lives elsewhere, add it under .claude/skills/<name>/SKILL.md in the repository (Claude) or the Codex skill directory.',
      'If the prompt no longer needs it, remove the reference.',
    ],
  },
  MISSING_SUBAGENT_REF: {
    what: 'A profile prompt names a subagent (@agent-name or `name` subagent) that is not defined in .claude/agents or an installed plugin, or the profile runs on Codex, which has no subagents.',
    howToFix: [
      'Check the name against the Subagents list above.',
      'Add the definition as .claude/agents/<name>.md with name and description frontmatter.',
      'For a Codex profile, drop the subagent reference or move the profile to Claude.',
    ],
  },
  USER_SCOPE_REF_ON_SSH: {
    what: "A referenced skill or subagent exists only in this machine's home directory or a plugin. Agents on SSH hosts see the repository's .claude/ directory, not your home, so it may be missing there.",
    howToFix: [
      "Claude profile: commit the skill or subagent under the repository's .claude/ directory so it travels with the repo.",
      'Codex profile: Codex reads no skills from the repository, so install the skill on every SSH host (for example under ~/.codex/skills/).',
      'Either backend: installing it on every SSH host also works.',
    ],
  },
  ORPHAN_MCP: {
    what: 'An MCP server is configured but its name is never mentioned in any skill name, description, or body. The tool schema is loaded into every agent context unconditionally — pure overhead if no skill knows when to call it.',
    howToFix: [
      'Either reference the server in a relevant skill body or description (so the AI invokes it deliberately) — or remove the MCP registration if no agent path needs it.',
      'For ambient servers (e.g. context7), this finding can be a false positive; future versions will let you whitelist them.',
    ],
  },
};
