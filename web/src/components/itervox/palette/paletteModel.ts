// Command palette model: the command shape, the groups, the scope prefixes
// and the ranking. Pure — no React, no stores — so the ranking is
// table-tested on its own.

import { fuzzyScore, normalizeId } from './fuzzy';

export type CommandGroup =
  | 'recent'
  | 'actions'
  | 'pages'
  | 'issues'
  | 'runs'
  | 'profiles'
  | 'automations'
  | 'rules'
  | 'memory'
  | 'settings'
  | 'sessions'
  | 'logs'
  | 'help';

export const GROUP_LABEL: Record<CommandGroup, string> = {
  recent: 'Recent',
  actions: 'Actions',
  pages: 'Go to',
  issues: 'Issues',
  runs: 'Running',
  profiles: 'Agent profiles',
  automations: 'Automations',
  rules: 'Rules',
  memory: 'Memory',
  settings: 'Settings',
  sessions: 'Sessions',
  logs: 'Logs',
  help: 'Search scopes',
};

export interface CommandConfirm {
  prompt: string;
  confirmLabel: string;
  /** The confirm button's tone; destructive unless set. */
  tone?: 'danger' | 'primary';
}

export interface Command {
  id: string;
  group: CommandGroup;
  label: string;
  /** A leading identifier, shown in the mono face; matched with an ID boost. */
  tag?: string;
  /** Extra text the query matches (an issue's title, a setting's key). */
  keywords?: string;
  /** Secondary text at the end of the row. */
  detail?: string;
  /** Destructive commands read in the danger tone. */
  danger?: boolean;
  /** The keyboard shortcut that does the same, shown inline. */
  hint?: string;
  /** Shown on the empty-query view. */
  top?: boolean;
  /** Picking it replaces the query with this text instead of running. */
  prefill?: string;
  run: () => void;
  confirm?: CommandConfirm;
}

export type PaletteScope = 'all' | 'actions' | 'issues' | 'profiles' | 'pages' | 'help';

export const SCOPE_PREFIXES: readonly {
  prefix: string;
  scope: Exclude<PaletteScope, 'all'>;
  label: string;
}[] = [
  { prefix: '>', scope: 'actions', label: 'Actions' },
  { prefix: '#', scope: 'issues', label: 'Issues' },
  { prefix: '@', scope: 'profiles', label: 'Agents and profiles' },
  { prefix: '/', scope: 'pages', label: 'Pages' },
  { prefix: '?', scope: 'help', label: 'Help: list these prefixes' },
];

const SCOPE_GROUPS: Record<Exclude<PaletteScope, 'all' | 'help'>, readonly CommandGroup[]> = {
  actions: ['actions', 'runs'],
  issues: ['issues', 'logs'],
  profiles: ['profiles'],
  pages: ['pages'],
};

/** Splits a leading scope prefix off the query. */
export function parseQuery(raw: string): { scope: PaletteScope; text: string } {
  const trimmed = raw.trimStart();
  const hit = SCOPE_PREFIXES.find((p) => trimmed.startsWith(p.prefix));
  if (!hit) return { scope: 'all', text: raw.trim() };
  return { scope: hit.scope, text: trimmed.slice(hit.prefix.length).trim() };
}

export function inScope(group: CommandGroup, scope: PaletteScope): boolean {
  if (scope === 'all') return group !== 'help';
  if (scope === 'help') return group === 'help';
  return SCOPE_GROUPS[scope].includes(group);
}

const ID_EXACT_BOOST = 30;
const ID_PREFIX_BOOST = 10;
const TAG_WEIGHT = 1.2;
/** The most recent command gets this much; each older one a step less. */
const RECENT_BOOST = 4;
const RECENT_STEP = 0.4;

function tokenScore(token: string, c: Command): number | null {
  let best: number | null = null;
  const consider = (s: number | null) => {
    if (s !== null && (best === null || s > best)) best = s;
  };
  consider(fuzzyScore(token, c.label));
  if (c.keywords) consider(fuzzyScore(token, c.keywords));
  if (c.tag) {
    const s = fuzzyScore(token, c.tag);
    if (s !== null) {
      const id = normalizeId(c.tag);
      const want = normalizeId(token);
      const boost =
        want !== '' && id === want
          ? ID_EXACT_BOOST
          : want !== '' && id.startsWith(want)
            ? ID_PREFIX_BOOST
            : 0;
      consider(s * TAG_WEIGHT + boost);
    }
  }
  return best;
}

/**
 * The command's score for the query's words (every word must match the
 * label, the tag or the keywords), or null when one does not.
 */
export function scoreCommand(text: string, c: Command): number | null {
  const words = text.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return 0;
  let total = 0;
  for (const w of words) {
    const s = tokenScore(w, c);
    if (s === null) return null;
    total += s;
  }
  return total;
}

function recentBoost(id: string, recents: readonly string[]): number {
  const i = recents.indexOf(id);
  return i < 0 ? 0 : Math.max(0, RECENT_BOOST - i * RECENT_STEP);
}

/**
 * The palette's rows for a query, in display order.
 *
 * - Empty query (no prefix): the recent commands that still exist, then the
 *   top actions not already listed as recent.
 * - A prefix with no text: every command of that scope, in source order.
 * - Text: the matching commands of the scope, ranked by score plus a boost
 *   for recently used ones, grouped — groups ordered by their best row, rows
 *   by score — with the "search logs" row last.
 */
export function rankCommands(
  commands: readonly Command[],
  rawQuery: string,
  recents: readonly string[],
): Command[] {
  const { scope, text } = parseQuery(rawQuery);
  if (scope === 'all' && text === '') {
    const byId = new Map(commands.map((c) => [c.id, c]));
    const recent = recents
      .map((id) => byId.get(id))
      .filter((c): c is Command => c !== undefined && c.group !== 'help')
      .map((c) => ({ ...c, group: 'recent' as const }));
    const shown = new Set(recent.map((c) => c.id));
    const top = commands.filter((c) => c.top && !shown.has(c.id));
    return [...recent, ...top];
  }
  const scoped = commands.filter((c) => inScope(c.group, scope));
  if (text === '') return scoped;

  const scored: { c: Command; s: number; i: number }[] = [];
  const last: Command[] = [];
  scoped.forEach((c, i) => {
    if (c.group === 'logs') {
      last.push(c);
      return;
    }
    const s = scoreCommand(text, c);
    if (s !== null) scored.push({ c, s: s + recentBoost(c.id, recents), i });
  });
  scored.sort((a, b) => b.s - a.s || a.i - b.i);
  const groupBest = new Map<CommandGroup, number>();
  for (const { c, s } of scored) {
    if (!groupBest.has(c.group)) groupBest.set(c.group, s);
  }
  const order = [...groupBest.keys()];
  scored.sort(
    (a, b) => order.indexOf(a.c.group) - order.indexOf(b.c.group) || b.s - a.s || a.i - b.i,
  );
  return [...scored.map((x) => x.c), ...last];
}
