// Shared labels and formatters for the skills inventory surfaces on the Repo
// Analysis page (#200; moved from Settings → SkillsCard).

import type { InventoryFix, SkillsInventory } from '../../../types/schemas';

export type SectionKey = 'skills' | 'subagents' | 'plugins' | 'mcp' | 'hooks' | 'instructions';

export const SECTION_KEYS: readonly SectionKey[] = [
  'skills',
  'subagents',
  'plugins',
  'mcp',
  'hooks',
  'instructions',
];

export const SECTION_LABELS: Record<SectionKey, string> = {
  skills: 'Skills',
  subagents: 'Subagents',
  plugins: 'Plugins',
  mcp: 'MCP Servers',
  hooks: 'Hooks',
  instructions: 'Instructions',
};

export const SEVERITY_BG: Record<string, string> = {
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

export function labelSource(source: string): string {
  if (SOURCE_LABELS[source]) return SOURCE_LABELS[source];
  if (source.startsWith('plugin:')) return `Plugin (${source.slice('plugin:'.length)})`;
  if (source.startsWith('marketplace:'))
    return `Marketplace (${source.slice('marketplace:'.length)})`;
  return source;
}

export function classifyProvider(s: {
  Provider: string;
  Source: string;
}): 'claude' | 'codex' | 'shared' {
  // Trust Source first — it carries higher-resolution truth than Provider.
  if (s.Source.includes('codex') || s.Source === 'superpowers') return 'codex';
  if (s.Source === 'shared-agents') return 'shared';
  return s.Provider === 'codex' ? 'codex' : 'claude';
}

export function fmtTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 10000) return `${(n / 1000).toFixed(1)}k`;
  return `${String(Math.round(n / 1000))}k`;
}

/** The Fix descriptor a recommendation row can apply. */
export type FixDescriptor = Pick<InventoryFix, 'Label' | 'Action' | 'Target' | 'Destructive'>;

/** Per-section counts and approximate context tokens of an inventory. */
export function inventoryTotals(inventory: SkillsInventory): {
  counts: Record<SectionKey, number>;
  tokens: Record<SectionKey, number>;
} {
  const counts: Record<SectionKey, number> = {
    skills: inventory.Skills?.length ?? 0,
    subagents: inventory.Subagents?.length ?? 0,
    plugins: inventory.Plugins?.length ?? 0,
    mcp: inventory.MCPServers?.length ?? 0,
    hooks: inventory.Hooks?.length ?? 0,
    instructions: inventory.Instructions?.length ?? 0,
  };
  const tokens: Record<SectionKey, number> = {
    skills: (inventory.Skills ?? []).reduce((sum, s) => sum + s.ApproxTokens, 0),
    // Only a subagent's name and description sit in the main context; its
    // body loads when the subagent is invoked.
    subagents: (inventory.Subagents ?? []).reduce(
      (sum, a) => sum + Math.ceil(((a.Description ?? '').length + a.Name.length) / 4),
      0,
    ),
    plugins: (inventory.Plugins ?? []).reduce((sum, p) => sum + p.ApproxTokens, 0),
    mcp: counts.mcp * 800,
    hooks: (inventory.Hooks ?? []).reduce((sum, h) => sum + h.ApproxTokens, 0),
    instructions: (inventory.Instructions ?? []).reduce((sum, d) => sum + d.ApproxTokens, 0),
  };
  return { counts, tokens };
}
