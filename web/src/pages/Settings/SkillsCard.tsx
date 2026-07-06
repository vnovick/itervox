// SkillsCard — Settings-page surface for the skills inventory.
//
// Sections:
//   1. Header + Re-scan
//   2. Recommendations (accordion: click for optimization guidance)
//   3. Capability tiles + expandable lists
//   4. Per-provider split (claude/codex/shared)
//   5. Runtime analytics
//   6. Context budget bars

import { useMemo, useState } from 'react';
import {
  useSkillsInventory,
  useSkillsIssues,
  useSkillsScan,
  useSkillsFix,
  useSkillsAnalytics,
  useSkillsAnalyticsRecommendations,
} from '../../queries/skills';
import type { Skill } from '../../types/schemas';

type SectionKey = 'skills' | 'plugins' | 'mcp' | 'hooks' | 'instructions';

const SECTION_LABELS: Record<SectionKey, string> = {
  skills: 'Skills',
  plugins: 'Plugins',
  mcp: 'MCP Servers',
  hooks: 'Hooks',
  instructions: 'Instructions',
};

const SEVERITY_BG: Record<string, string> = {
  error: 'bg-red-500/15 text-red-400 border-red-400/40',
  warn: 'bg-yellow-500/15 text-yellow-400 border-yellow-400/40',
  info: 'bg-blue-500/15 text-blue-400 border-blue-400/40',
};

// Display-time relabeling. The Go scanner's `Source` strings are designed
// for diffing/grouping; this map turns them into operator-friendly labels
// that say which provider owns the path.
const SOURCE_LABELS: Record<string, string> = {
  user: 'Claude (user)',
  project: 'Claude (project)',
  'user-codex': 'Codex (user)',
  'system-codex': 'Codex (system)',
  superpowers: 'Codex (superpowers)',
  'shared-agents': 'Shared (~/.agents)',
  system: 'Claude (system)',
};

function labelSource(source: string): string {
  if (SOURCE_LABELS[source]) return SOURCE_LABELS[source];
  if (source.startsWith('plugin:')) return `Plugin (${source.slice('plugin:'.length)})`;
  if (source.startsWith('marketplace:'))
    return `Marketplace (${source.slice('marketplace:'.length)})`;
  return source;
}

function classifyProvider(s: { Provider: string; Source: string }): 'claude' | 'codex' | 'shared' {
  // Trust Source first — it carries higher-resolution truth than Provider.
  if (s.Source.includes('codex') || s.Source === 'superpowers') return 'codex';
  if (s.Source === 'shared-agents') return 'shared';
  return s.Provider === 'codex' ? 'codex' : 'claude';
}

// Per-recommendation help — shown when a row is expanded.
const RECOMMENDATION_HELP: Partial<Record<string, { what: string; howToFix: string[] }>> = {
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
  ORPHAN_MCP: {
    what: 'An MCP server is configured but its name is never mentioned in any skill name, description, or body. The tool schema is loaded into every agent context unconditionally — pure overhead if no skill knows when to call it.',
    howToFix: [
      'Either reference the server in a relevant skill body or description (so the AI invokes it deliberately) — or remove the MCP registration if no agent path needs it.',
      'For ambient servers (e.g. context7), this finding can be a false positive; future versions will let you whitelist them.',
    ],
  },
};

function fmtTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 10000) return `${(n / 1000).toFixed(1)}k`;
  return `${String(Math.round(n / 1000))}k`;
}

export function SkillsCard() {
  const { data: inventory, isLoading, error } = useSkillsInventory();
  const { data: issues = [] } = useSkillsIssues();
  const scan = useSkillsScan();
  const fix = useSkillsFix();
  const [expanded, setExpanded] = useState<SectionKey | null>(null);

  function applyFix(
    issueID: string,
    f: { Label: string; Action: string; Target?: string; Destructive: boolean },
  ) {
    if (f.Destructive) {
      const ok = window.confirm(
        `${f.Label}\n\nThis is a destructive action and will edit configuration. Continue?`,
      );
      if (!ok) return;
    }
    fix.mutate({ issueID, fix: f });
  }

  if (isLoading) {
    return (
      <div className="border-theme-line bg-theme-panel rounded-lg border p-4 text-sm">
        Loading skills inventory…
      </div>
    );
  }
  if (error) {
    return (
      <div className="border-theme-danger-soft bg-theme-danger-soft text-theme-danger rounded-lg border p-4 text-sm">
        Failed to load skills inventory: {String(error)}
      </div>
    );
  }
  if (!inventory) {
    return (
      <div className="border-theme-line bg-theme-panel rounded-lg border p-4 text-sm">
        <p className="mb-3">Inventory has not been scanned yet.</p>
        <button
          onClick={() => {
            scan.mutate();
          }}
          disabled={scan.isPending}
          className="bg-theme-accent rounded px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-50"
        >
          {scan.isPending ? 'Scanning…' : 'Run first scan'}
        </button>
      </div>
    );
  }

  const counts: Record<SectionKey, number> = {
    skills: inventory.Skills?.length ?? 0,
    plugins: inventory.Plugins?.length ?? 0,
    mcp: inventory.MCPServers?.length ?? 0,
    hooks: inventory.Hooks?.length ?? 0,
    instructions: inventory.Instructions?.length ?? 0,
  };

  const tokens: Record<SectionKey, number> = {
    skills: (inventory.Skills ?? []).reduce((sum, s) => sum + s.ApproxTokens, 0),
    plugins: (inventory.Plugins ?? []).reduce((sum, p) => sum + p.ApproxTokens, 0),
    mcp: counts.mcp * 800,
    hooks: (inventory.Hooks ?? []).reduce((sum, h) => sum + h.ApproxTokens, 0),
    instructions: (inventory.Instructions ?? []).reduce((sum, d) => sum + d.ApproxTokens, 0),
  };
  const totalTokens = Object.values(tokens).reduce((s, n) => s + n, 0);
  const maxTokens = Math.max(1, ...Object.values(tokens));
  const inventoryStale = inventory.Stale === true;

  return (
    <div className="space-y-6">
      {/* Header + Re-scan */}
      <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
        <div className="flex items-center justify-between gap-3">
          <div>
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-theme-text text-sm font-medium">Inventory</p>
              <span
                role="status"
                className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-bold uppercase ${
                  inventoryStale
                    ? 'border-yellow-400/40 bg-yellow-500/15 text-yellow-400'
                    : 'border-emerald-400/40 bg-emerald-500/15 text-emerald-400'
                }`}
              >
                {inventoryStale ? 'Stale' : 'Tracked files current'}
              </span>
            </div>
            <p className="text-theme-muted mt-0.5 text-xs">
              Last scanned: {new Date(inventory.ScanTime).toLocaleString()}
            </p>
            <p className="text-theme-muted mt-1 max-w-xl text-[11px]">
              {inventoryStale
                ? 'Tracked config or inventory files changed since the last scan. Click Re-scan to refresh recommendations.'
                : 'Tracked config and inventory files are unchanged since the last scan. Click Re-scan after adding new roots or changing untracked paths.'}
            </p>
          </div>
          <button
            onClick={() => {
              scan.mutate();
            }}
            disabled={scan.isPending}
            className="border-theme-line text-theme-text rounded border px-3 py-1.5 text-xs font-semibold hover:bg-[var(--bg-soft)] disabled:opacity-50"
          >
            {scan.isPending ? 'Scanning…' : 'Re-scan'}
          </button>
        </div>
      </div>

      {inventory.Partial && (
        <div
          role="status"
          className="border-theme-warning-soft bg-theme-warning-soft text-theme-warning rounded-lg border p-4 text-sm"
        >
          <p className="font-medium">Partial inventory</p>
          <p className="mt-1 text-xs">
            {inventory.ScanError || 'One or more scanners failed; showing best-effort results.'}
          </p>
        </div>
      )}

      {/* Recommendations — accordion with optimization help */}
      <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
        <p className="text-theme-text mb-2 text-sm font-medium">
          Recommendations <span className="text-theme-muted text-xs">({issues.length})</span>
        </p>
        <p className="text-theme-muted mb-3 text-[11px]">
          Click any recommendation to expand for optimization guidance.
        </p>
        {issues.length === 0 ? (
          <p className="text-theme-muted text-xs">No issues — your inventory looks clean.</p>
        ) : (
          <ul className="space-y-2">
            {issues.map((issue, idx) => (
              <RecommendationRow
                key={`${issue.ID}-${(issue.Affected ?? []).join(',')}-${String(idx)}`}
                issue={issue}
                onApplyFix={applyFix}
                fixPending={fix.isPending}
              />
            ))}
          </ul>
        )}
      </div>

      {/* Capability catalog */}
      <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
        <p className="text-theme-text mb-2 text-sm font-medium">Capabilities</p>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-5">
          {(Object.keys(counts) as SectionKey[]).map((key) => (
            <button
              key={key}
              onClick={() => {
                setExpanded(expanded === key ? null : key);
              }}
              className={`rounded border px-3 py-2 text-left transition-colors ${
                expanded === key
                  ? 'border-theme-accent bg-theme-accent-soft'
                  : 'border-theme-line hover:bg-[var(--bg-soft)]'
              }`}
            >
              <p className="text-theme-muted text-[10px] font-semibold tracking-wider uppercase">
                {SECTION_LABELS[key]}
              </p>
              <p className="text-theme-text mt-0.5 text-lg font-bold tabular-nums">{counts[key]}</p>
              <p className="text-theme-muted text-[10px]">{fmtTokens(tokens[key])} tok</p>
            </button>
          ))}
        </div>

        {expanded && <ExpandedSection sectionKey={expanded} inventory={inventory} />}
      </div>

      {/* Per-provider breakdown */}
      <ProviderBreakdown inventory={inventory} />

      {/* Runtime analytics */}
      <AnalyticsSection />

      {/* Context budget bars */}
      <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
        <p className="text-theme-text mb-2 text-sm font-medium">
          Context budget{' '}
          <span className="text-theme-muted text-xs">(approx {fmtTokens(totalTokens)} total)</span>
        </p>
        <div className="space-y-2">
          {(Object.keys(tokens) as SectionKey[]).map((key) => {
            const pct = Math.round((tokens[key] / maxTokens) * 100);
            return (
              <div key={key} className="flex items-center gap-2 text-xs">
                <span className="text-theme-muted w-24 flex-shrink-0">{SECTION_LABELS[key]}</span>
                <div className="bg-theme-bg-elevated relative h-3 flex-1 overflow-hidden rounded">
                  <div
                    className="bg-theme-accent absolute inset-y-0 left-0"
                    style={{ width: `${String(pct)}%` }}
                  />
                </div>
                <span className="text-theme-muted w-12 flex-shrink-0 text-right tabular-nums">
                  {fmtTokens(tokens[key])}
                </span>
              </div>
            );
          })}
        </div>
        <p className="text-theme-muted mt-3 text-[10px] italic">
          Token counts are approximate (skill body bytes ÷ 4, 800 × MCP server count, hook command
          bytes × 2 ÷ 4). Useful for ratio comparisons, not absolute claims. Run sessions with{' '}
          <code className="font-mono">CLAUDE_CODE_LOG_DIR</code> set to refine the MCP figure with
          observed tool loads.
        </p>
      </div>
    </div>
  );
}

function RecommendationRow({
  issue,
  onApplyFix,
  fixPending,
}: {
  issue: {
    ID: string;
    Severity: string;
    Title: string;
    Description: string;
    Affected?: string[] | null;
    Fix?: { Label: string; Action: string; Target?: string; Destructive: boolean } | null;
  };
  onApplyFix: (
    id: string,
    f: { Label: string; Action: string; Target?: string; Destructive: boolean },
  ) => void;
  fixPending: boolean;
}) {
  const [open, setOpen] = useState(false);
  const help = RECOMMENDATION_HELP[issue.ID];

  // The header is a plain clickable div (no ARIA role) so its accessible
  // name doesn't shadow the inner Fix <button>. A dedicated toggle <button>
  // on the right edge handles keyboard a11y; the rest of the row mirrors
  // the same click for affordance.
  return (
    <li className="border-theme-line rounded border">
      <div
        onClick={() => {
          setOpen((v) => !v);
        }}
        className="flex w-full cursor-pointer items-start justify-between gap-3 p-2 text-left hover:bg-[var(--bg-soft)]"
      >
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span
              className={`inline-flex items-center rounded border px-1.5 py-0.5 text-[10px] font-bold uppercase ${
                SEVERITY_BG[issue.Severity] ?? SEVERITY_BG.info
              }`}
            >
              {issue.Severity}
            </span>
            <span className="text-theme-text text-xs font-semibold">{issue.Title}</span>
            <button
              type="button"
              aria-expanded={open}
              aria-label={open ? 'Collapse details' : 'Expand details'}
              onClick={(e) => {
                e.stopPropagation();
                setOpen((v) => !v);
              }}
              className="text-theme-muted ml-auto rounded px-1 text-[10px] hover:bg-[var(--bg-soft)]"
            >
              {open ? '▼' : '▶'}
            </button>
          </div>
          <p className="text-theme-muted mt-1 text-xs">{issue.Description}</p>
        </div>
        {issue.Fix && (
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              if (issue.Fix) onApplyFix(issue.ID, issue.Fix);
            }}
            disabled={fixPending}
            title={issue.Fix.Destructive ? 'Destructive — confirms before running' : 'Apply fix'}
            className={`flex-shrink-0 rounded border px-2 py-1 text-[10px] font-semibold disabled:opacity-50 ${
              issue.Fix.Destructive
                ? 'border-red-400/40 text-red-400 hover:bg-red-500/10'
                : 'border-theme-line text-theme-text hover:bg-[var(--bg-soft)]'
            }`}
          >
            {issue.Fix.Label}
          </button>
        )}
      </div>

      {open && (
        <div className="border-theme-line border-t bg-[var(--bg-soft)] p-3 text-xs">
          {help ? (
            <>
              <p className="text-theme-text mb-2 font-semibold">What this means</p>
              <p className="text-theme-muted mb-3">{help.what}</p>
              <p className="text-theme-text mb-2 font-semibold">How to optimize</p>
              <ol className="text-theme-muted list-decimal space-y-1 pl-4">
                {help.howToFix.map((step, i) => (
                  <li key={i}>{step}</li>
                ))}
              </ol>
            </>
          ) : (
            <p className="text-theme-muted italic">No optimization guidance for {issue.ID} yet.</p>
          )}

          {(issue.Affected ?? []).length > 0 && (
            <>
              <p className="text-theme-text mt-3 mb-1 font-semibold">Affected</p>
              <ul className="text-theme-muted font-mono text-[11px]">
                {(issue.Affected ?? []).map((a, i) => (
                  <li key={i}>· {a}</li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}
    </li>
  );
}

function ProviderBreakdown({
  inventory,
}: {
  inventory: NonNullable<ReturnType<typeof useSkillsInventory>['data']>;
}) {
  const stats = useMemo(() => {
    const allSkills: Skill[] = [...(inventory.Skills ?? [])];
    for (const p of inventory.Plugins ?? []) allSkills.push(...(p.Skills ?? []));

    const byProvider = {
      claude: { count: 0, tokens: 0 },
      codex: { count: 0, tokens: 0 },
      shared: { count: 0, tokens: 0 },
    };
    for (const s of allSkills) {
      const provider = classifyProvider(s);
      byProvider[provider].count += 1;
      byProvider[provider].tokens += s.ApproxTokens;
    }
    return byProvider;
  }, [inventory]);

  const total = stats.claude.tokens + stats.codex.tokens + stats.shared.tokens;
  const max = Math.max(1, stats.claude.tokens, stats.codex.tokens, stats.shared.tokens);

  return (
    <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
      <p className="text-theme-text mb-2 text-sm font-medium">
        By provider <span className="text-theme-muted text-xs">(skill cost split)</span>
      </p>
      <div className="space-y-2">
        {(['claude', 'codex', 'shared'] as const).map((p) => {
          const s = stats[p];
          const pct = Math.round((s.tokens / max) * 100);
          return (
            <div key={p} className="flex items-center gap-2 text-xs">
              <span className="text-theme-muted w-24 flex-shrink-0 capitalize">{p}</span>
              <span className="text-theme-text w-10 flex-shrink-0 tabular-nums">{s.count}</span>
              <div className="bg-theme-bg-elevated relative h-3 flex-1 overflow-hidden rounded">
                <div
                  className="bg-theme-accent absolute inset-y-0 left-0"
                  style={{ width: `${String(pct)}%` }}
                />
              </div>
              <span className="text-theme-muted w-12 flex-shrink-0 text-right tabular-nums">
                {fmtTokens(s.tokens)}
              </span>
            </div>
          );
        })}
      </div>
      <p className="text-theme-muted mt-3 text-[10px] italic">
        Total skill cost: {fmtTokens(total)}. Provider is inferred from the source path:{' '}
        <code className="font-mono">~/.claude/skills/</code> = claude,{' '}
        <code className="font-mono">~/.codex/skills/</code> = codex,{' '}
        <code className="font-mono">~/.agents/skills/</code> = shared (loaded by both).
      </p>
    </div>
  );
}

function AnalyticsSection() {
  const { data: analytics, isLoading } = useSkillsAnalytics();
  const { data: recs = [] } = useSkillsAnalyticsRecommendations();

  if (isLoading) return null;
  if (!analytics || analytics.HasRuntimeEvidence === false) {
    return (
      <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
        <p className="text-theme-text mb-1 text-sm font-medium">Runtime analytics</p>
        <p className="text-theme-muted text-xs">
          Runtime evidence not yet available — set{' '}
          <code className="font-mono">CLAUDE_CODE_LOG_DIR</code> and run a few sessions to populate.
        </p>
      </div>
    );
  }

  const verifiedCount = (analytics.SkillStats ?? []).filter((s) => s.RuntimeVerified).length;
  const totalSkills = (analytics.SkillStats ?? []).length;
  const hookRuntimeCount = (analytics.HookStats ?? []).filter(
    (h) => (h.RuntimeLoads ?? 0) > 0,
  ).length;

  return (
    <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
      <p className="text-theme-text mb-2 text-sm font-medium">
        Runtime analytics{' '}
        <span className="text-theme-muted text-xs">
          (last scan {new Date(analytics.GeneratedAt).toLocaleTimeString()})
        </span>
      </p>

      <div className="text-theme-muted mb-3 grid grid-cols-3 gap-2 text-xs">
        <div>
          <span className="text-theme-text font-bold tabular-nums">{verifiedCount}</span> /{' '}
          {totalSkills} skills runtime-verified
        </div>
        <div>
          <span className="text-theme-text font-bold tabular-nums">{hookRuntimeCount}</span> hooks
          with activity
        </div>
        <div>
          <span className="text-theme-text font-bold tabular-nums">{recs.length}</span>{' '}
          recommendations
        </div>
      </div>

      {recs.length === 0 ? (
        <p className="text-theme-muted text-xs">No analytics recommendations.</p>
      ) : (
        <ul className="space-y-2">
          {recs.slice(0, 12).map((r, idx) => (
            <RecommendationRow
              key={`${r.ID}-${(r.Affected ?? []).join(',')}-${String(idx)}`}
              issue={{
                ID: r.ID,
                Severity: r.Severity,
                Title: r.Title,
                Description: r.Description,
                Affected: r.Affected,
              }}
              onApplyFix={() => {
                /* analytics recs have no inline fixes today */
              }}
              fixPending={false}
            />
          ))}
        </ul>
      )}
    </div>
  );
}

function ExpandedSection({
  sectionKey,
  inventory,
}: {
  sectionKey: SectionKey;
  inventory: NonNullable<ReturnType<typeof useSkillsInventory>['data']>;
}) {
  switch (sectionKey) {
    case 'skills': {
      const items = inventory.Skills ?? [];
      return (
        <ul className="border-theme-line mt-3 space-y-1 border-t pt-3">
          {items.map((s, i) => (
            <li
              key={`${s.Name}-${String(i)}`}
              className="flex items-center justify-between gap-2 text-xs"
            >
              <span className="text-theme-text font-mono" title={s.FilePath}>
                {s.Name}
              </span>
              <span className="text-theme-muted truncate">
                {labelSource(s.Source)} · {fmtTokens(s.ApproxTokens)} tok
              </span>
            </li>
          ))}
          {items.length === 0 && (
            <li className="text-theme-muted text-xs">No skills configured.</li>
          )}
        </ul>
      );
    }
    case 'plugins': {
      const items = inventory.Plugins ?? [];
      return (
        <ul className="border-theme-line mt-3 space-y-1 border-t pt-3">
          {items.map((p, i) => (
            <li
              key={`${p.Name}-${String(i)}`}
              className="flex items-center justify-between gap-2 text-xs"
            >
              <span className="text-theme-text font-mono">{p.Name}</span>
              <span className="text-theme-muted">
                {labelSource(p.Source)} · {(p.Skills ?? []).length} skills ·{' '}
                {(p.Hooks ?? []).length} hooks · {fmtTokens(p.ApproxTokens)} tok
              </span>
            </li>
          ))}
          {items.length === 0 && (
            <li className="text-theme-muted text-xs">No plugins configured.</li>
          )}
        </ul>
      );
    }
    case 'mcp': {
      const items = inventory.MCPServers ?? [];
      return (
        <ul className="border-theme-line mt-3 space-y-1 border-t pt-3">
          {items.map((srv, i) => (
            <li
              key={`${srv.Name}-${String(i)}`}
              className="flex items-center justify-between gap-2 text-xs"
            >
              <span className="text-theme-text font-mono">{srv.Name}</span>
              <span className="text-theme-muted">
                {srv.Transport ?? '—'} · {labelSource(srv.Source)}
              </span>
            </li>
          ))}
          {items.length === 0 && (
            <li className="text-theme-muted text-xs">No MCP servers configured.</li>
          )}
        </ul>
      );
    }
    case 'hooks': {
      const items = inventory.Hooks ?? [];
      return (
        <ul className="border-theme-line mt-3 space-y-1 border-t pt-3">
          {items.map((h, i) => (
            <li key={`${h.Event}-${String(i)}`} className="text-xs">
              <span className="text-theme-text font-mono">{h.Event}</span>
              {h.Matcher && <span className="text-theme-muted"> · matcher: {h.Matcher}</span>}
              <span className="text-theme-muted"> · {labelSource(h.Source)}</span>
              <div className="text-theme-muted truncate font-mono text-[10px]">{h.Command}</div>
            </li>
          ))}
          {items.length === 0 && <li className="text-theme-muted text-xs">No hooks configured.</li>}
        </ul>
      );
    }
    case 'instructions': {
      const items = inventory.Instructions ?? [];
      return (
        <ul className="border-theme-line mt-3 space-y-1 border-t pt-3">
          {items.map((d, i) => (
            <li
              key={`${d.FilePath}-${String(i)}`}
              className="flex items-center justify-between gap-2 text-xs"
            >
              <span className="text-theme-text truncate font-mono" title={d.FilePath}>
                {d.FilePath}
              </span>
              <span className="text-theme-muted flex-shrink-0">
                {d.Provider} · {d.Scope} · {fmtTokens(d.ApproxTokens)} tok
              </span>
            </li>
          ))}
          {items.length === 0 && (
            <li className="text-theme-muted text-xs">No instruction docs found.</li>
          )}
        </ul>
      );
    }
  }
}
