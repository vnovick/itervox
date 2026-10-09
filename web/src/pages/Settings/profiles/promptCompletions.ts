// Suggestion logic for the prompt editor's autocomplete (#87): which
// completion context the caret is in, what to suggest, and how a choice is
// inserted. Pure functions; the component only wires them to a textarea.

import type { SkillsInventory } from '../../../types/schemas';

export type CompletionKind = 'skill' | 'subagent' | 'variable' | 'filter';

export interface Completion {
  /** What is inserted, e.g. `tdd`, `issue.title`, `downcase`. */
  label: string;
  kind: CompletionKind;
  /** One-line description. */
  detail?: string;
  /** Where it comes from: project, user, plugin:<name>, or Liquid. */
  source?: string;
}

/** The completion the caret is in. `start` is where the typed query begins. */
export interface CompletionContext {
  kind: 'reference' | 'variable' | 'filter';
  query: string;
  start: number;
  /** The character that opened a reference (`/` or `@`). */
  trigger?: '/' | '@';
}

const v = (label: string, detail: string): Completion => ({
  label,
  kind: 'variable',
  detail,
  source: 'Liquid',
});

/** The Liquid variables every profile prompt (SOUL, INSTRUCTIONS) gets. */
export const PROFILE_VARIABLES: readonly Completion[] = [
  v('issue.identifier', 'Issue identifier, e.g. ENG-12 or #42'),
  v('issue.title', 'Issue title'),
  v('issue.description', 'Issue description (Markdown)'),
  v('issue.state', 'Current tracker state'),
  v('issue.priority', 'Priority number (empty when unset)'),
  v('issue.labels', 'Labels, lower case'),
  v('issue.url', 'Link to the issue'),
  v('issue.branch_name', "The tracker's branch name for the issue"),
  v('issue.blocked_by', 'Blockers: id, identifier, state'),
  v('issue.comments', 'Comments: body, author_name, created_at'),
  v('issue.created_at', 'Creation time'),
  v('issue.updated_at', 'Last update time'),
  v('issue.id', "The tracker's internal issue ID"),
  v('attempt', 'Retry attempt number (empty on the first run)'),
  v('run.timestamp', "This run's timestamp"),
  v('run.handoff_path', 'Where this run writes its handoff file'),
  v('run.evidence_path', 'Where this run records evidence (require_evidence)'),
  v('run.pr_base_branch', 'Branch a pull request from this run targets'),
  v('run.previous_backend', 'Backend before a switch (empty otherwise)'),
  v('run.previous_profile', 'Profile before a switch (empty otherwise)'),
  v('run.switch_reason', 'Why the backend was switched'),
  v('run.limit_resets_at', "When the previous backend's limit resets"),
];

/** Extra variables for automation instructions. */
export const TRIGGER_VARIABLES: readonly Completion[] = [
  v('trigger.type', 'Trigger type, e.g. cron or issue_entered_state'),
  v('trigger.fired_at', 'When the trigger fired'),
  v('trigger.automation_id', 'The automation that fired'),
  v('trigger.current_state', 'Issue state when it fired'),
  v('trigger.previous_state', 'Issue state before the change'),
  v('trigger.input_context', 'The question for input_required triggers'),
  v('trigger.error_message', 'The failure for run_failed triggers'),
  v('trigger.comment.body', 'The comment for tracker_comment_added'),
  v('trigger.comment.author_name', "The comment's author"),
  v('trigger.pr_url', 'Pull request URL, when known'),
  v('trigger.pr_branch', "Pull request's branch"),
  v('trigger.pr_base_branch', "Pull request's base branch"),
  v('trigger.resolved_blockers', 'Blockers that just resolved (blockers_resolved)'),
];

const f = (label: string, detail: string): Completion => ({
  label,
  kind: 'filter',
  detail,
  source: 'Liquid',
});

/** Common Liquid filters, suggested after `|` inside `{{ … }}`. */
export const LIQUID_FILTERS: readonly Completion[] = [
  f('default', 'Fallback when empty: default: "none"'),
  f('downcase', 'Lower case'),
  f('upcase', 'Upper case'),
  f('replace', 'Replace text: replace: "#", ""'),
  f('remove', 'Remove text: remove: "#"'),
  f('strip', 'Trim whitespace'),
  f('truncate', 'Shorten: truncate: 200'),
  f('join', 'Join a list: join: ", "'),
  f('size', 'Length of a list or text'),
  f('first', 'First item of a list'),
  f('last', 'Last item of a list'),
  f('escape', 'HTML-escape'),
];

/** Characters a reference (skill or subagent name) may contain. */
const REFERENCE_RE = /(^|[\s(])([/@])([\w.:-]*)$/;

/** Returns the completion context at caret, or null when there is none. */
export function completionContext(text: string, caret: number): CompletionContext | null {
  const before = text.slice(0, caret);
  const open = before.lastIndexOf('{{');
  if (open >= 0 && !before.slice(open).includes('}}')) {
    const inner = before.slice(open + 2);
    if (inner.includes('\n')) return null;
    const pipe = inner.lastIndexOf('|');
    if (pipe >= 0) {
      const tail = inner.slice(pipe + 1);
      const query = tail.trimStart();
      if (/[^\w]/.test(query)) return null; // past the filter name (its arguments)
      return { kind: 'filter', query, start: caret - query.length };
    }
    const query = inner.trimStart();
    if (/[^\w.]/.test(query)) return null;
    return { kind: 'variable', query, start: caret - query.length };
  }
  const m = REFERENCE_RE.exec(before.slice(Math.max(0, caret - 80)));
  if (!m) return null;
  const query = m[3];
  return { kind: 'reference', query, start: caret - query.length, trigger: m[2] as '/' | '@' };
}

function firstLine(s?: string): string | undefined {
  const line = s?.split('\n').find((l) => l.trim() !== '');
  return line?.trim();
}

/** Skills and subagents from the inventory, deduplicated by kind and name. */
export function referenceCompletions(inv: SkillsInventory | null | undefined): Completion[] {
  if (!inv) return [];
  const out: Completion[] = [];
  const seen = new Set<string>();
  const add = (c: Completion) => {
    const key = `${c.kind}:${c.label}`;
    if (seen.has(key)) return;
    seen.add(key);
    out.push(c);
  };
  for (const s of inv.Skills ?? []) {
    add({ label: s.Name, kind: 'skill', detail: firstLine(s.Description), source: s.Source });
  }
  for (const a of inv.Subagents ?? []) {
    add({ label: a.Name, kind: 'subagent', detail: firstLine(a.Description), source: a.Source });
  }
  for (const p of inv.Plugins ?? []) {
    const source = `plugin:${p.Name}`;
    for (const s of p.Skills ?? []) {
      add({ label: s.Name, kind: 'skill', detail: firstLine(s.Description), source });
    }
    for (const a of p.Agents ?? []) {
      add({ label: a.Name, kind: 'subagent', detail: firstLine(a.Description), source });
    }
  }
  return out;
}

/** The candidates for a context. */
export function candidatesFor(
  ctx: CompletionContext,
  references: readonly Completion[],
  variables: readonly Completion[],
): readonly Completion[] {
  switch (ctx.kind) {
    case 'reference':
      return references;
    case 'variable':
      return variables;
    case 'filter':
      return LIQUID_FILTERS;
  }
}

/** Matching candidates: prefix matches first, then substring matches. */
export function matchCompletions(
  items: readonly Completion[],
  query: string,
  limit = 8,
): Completion[] {
  const q = query.toLowerCase();
  const prefix: Completion[] = [];
  const contains: Completion[] = [];
  for (const item of items) {
    const label = item.label.toLowerCase();
    if (label.startsWith(q)) prefix.push(item);
    else if (q !== '' && label.includes(q)) contains.push(item);
  }
  return [...prefix, ...contains].slice(0, limit);
}

/** Inserts item for ctx, returning the new text and caret. */
export function applyCompletion(
  text: string,
  caret: number,
  ctx: CompletionContext,
  item: Completion,
  selectionEnd = caret,
): { text: string; caret: number } {
  let insert = item.label;
  // Replace the selection and the rest of the word the caret is in, so a
  // caret inside `/td|d` or `{{ |issue.ti` never leaves the old text behind.
  const end = Math.max(caret, selectionEnd);
  const word = /^[\w.:-]*/.exec(text.slice(end))?.[0] ?? '';
  const after = text.slice(end + word.length);
  if (ctx.kind === 'variable' || ctx.kind === 'filter') {
    const lead = text.slice(0, ctx.start);
    if (lead.endsWith('{{') || lead.endsWith('|')) insert = ' ' + insert;
    if (ctx.kind === 'variable' && !/^\s*(\||}})/.test(after)) insert += ' }}';
  } else if (!/^\s/.test(after)) {
    insert += ' ';
  }
  const next = text.slice(0, ctx.start) + insert + after;
  return { text: next, caret: ctx.start + insert.length };
}
