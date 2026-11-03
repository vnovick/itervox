// Backend-aware view of the skills inventory (#123). One place decides which
// inventory items a backend can use, so the prompt editor's suggestions and
// the pickers agree. The rules mirror the daemon's reference validator
// (internal/skills/validate_refs.go resolvableNames), which doctor and the
// editor's underlines use:
//
//   - skills: Provider "claude" → Claude, "codex" → Codex, "shared"/"" → both
//     (a plugin's skills follow the plugin's provider);
//   - subagents: Claude only (Codex has no subagents);
//   - prompts (~/.codex/prompts, run as /prompts:<name>): Codex only.

import type { SkillsInventory } from '../../../types/schemas';
import {
  inferBackendFromCommand,
  normalizeBackend,
  type SupportedBackend,
} from '../profileCommands';

export type InventoryItemKind = 'skill' | 'subagent' | 'prompt';

export interface BackendItem {
  kind: InventoryItemKind;
  name: string;
  description?: string;
  /** project, user, plugin:<name>, user-codex, … */
  source: string;
}

/** Whether an item with this provider can be used by backend. */
export function providerFits(provider: string | undefined, backend: SupportedBackend): boolean {
  return !provider || provider === 'shared' || provider === backend;
}

function firstLine(s?: string): string | undefined {
  const line = s?.split('\n').find((l) => l.trim() !== '');
  return line?.trim();
}

/**
 * The inventory items backend can use, de-duplicated by kind and name, in
 * order: skills, subagents, prompts. With no backend every item is listed.
 */
export function backendItems(
  inv: SkillsInventory | null | undefined,
  backend?: SupportedBackend,
): BackendItem[] {
  if (!inv) return [];
  const fits = (provider: string | undefined) => !backend || providerFits(provider, backend);
  const out: BackendItem[] = [];
  const seen = new Set<string>();
  const add = (kind: InventoryItemKind, name: string, source: string, description?: string) => {
    const key = `${kind}:${name}`;
    if (seen.has(key)) return;
    seen.add(key);
    out.push({ kind, name, source, description: firstLine(description) });
  };
  for (const s of inv.Skills ?? []) {
    if (fits(s.Provider)) add('skill', s.Name, s.Source, s.Description);
  }
  for (const p of inv.Plugins ?? []) {
    if (!fits(p.Provider)) continue;
    for (const s of p.Skills ?? []) add('skill', s.Name, `plugin:${p.Name}`, s.Description);
  }
  if (backend !== 'codex') {
    for (const a of inv.Subagents ?? []) add('subagent', a.Name, a.Source, a.Description);
    for (const p of inv.Plugins ?? []) {
      if (p.Provider && p.Provider !== 'claude') continue;
      for (const a of p.Agents ?? []) add('subagent', a.Name, `plugin:${p.Name}`, a.Description);
    }
  }
  if (backend !== 'claude') {
    for (const pr of inv.Prompts ?? []) {
      if (fits(pr.Provider)) add('prompt', pr.Name, pr.Source, pr.Description);
    }
  }
  return out;
}

/** How a prompt names item so the validator (and the backend) recognise it. */
export function referenceText(item: Pick<BackendItem, 'kind' | 'name'>): string {
  switch (item.kind) {
    case 'skill':
      return `Use the \`${item.name}\` skill.`;
    case 'subagent':
      return `@agent-${item.name}`;
    case 'prompt':
      return `/prompts:${item.name}`;
  }
}

/**
 * A best guess at a draft profile's backend before the daemon has resolved
 * it: a recognised command binary wins (as in the runner), then the
 * selected backend, then fallback (the workflow default).
 */
export function guessProfileBackend(
  command: string | undefined,
  backend: string | undefined,
  fallback: SupportedBackend = 'claude',
): SupportedBackend {
  const fromCommand = inferBackendFromCommand(command);
  if (fromCommand) return fromCommand;
  return backend ? normalizeBackend(backend) : fallback;
}

/** Counts per kind, for a one-line summary. */
export function itemCounts(items: readonly BackendItem[]): Record<InventoryItemKind, number> {
  const counts: Record<InventoryItemKind, number> = { skill: 0, subagent: 0, prompt: 0 };
  for (const item of items) counts[item.kind] += 1;
  return counts;
}

/** "12 skills, 3 subagents" — kinds with no items are left out. */
export function describeCounts(counts: Record<InventoryItemKind, number>): string {
  const parts: string[] = [];
  const plural = (n: number, word: string) => `${String(n)} ${word}${n === 1 ? '' : 's'}`;
  if (counts.skill) parts.push(plural(counts.skill, 'skill'));
  if (counts.subagent) parts.push(plural(counts.subagent, 'subagent'));
  if (counts.prompt) parts.push(plural(counts.prompt, 'prompt'));
  return parts.length ? parts.join(', ') : 'no skills found';
}

/** Appends a reference on its own line at the end of text. */
export function appendReference(text: string, reference: string): string {
  const trimmed = text.replace(/\s+$/, '');
  return `${trimmed}${trimmed ? '\n\n' : ''}${reference}\n`;
}
