// Rules editor helpers (#157), shared by the page's components.

import { RuleSchema, type Rule, type RulesLayerName } from '../../types/schemas';

export const LAYER_INFO: Record<RulesLayerName, { label: string; git: string }> = {
  user: {
    label: 'User',
    git: 'Yours, on this machine: applies to every project. Never committed.',
  },
  project: { label: 'Project', git: 'Committed with the code: the team’s rules for this project.' },
  local: { label: 'Project local', git: 'Yours, for this project only. Gitignored.' },
};

const LAYER_LABELS: Partial<Record<string, string>> = {
  user: LAYER_INFO.user.label,
  project: LAYER_INFO.project.label,
  local: LAYER_INFO.local.label,
};

/** The editor's name for a layer the daemon reports ("local" → "Project local"). */
export function layerLabel(name: string): string {
  return LAYER_LABELS[name] ?? name;
}

/** What the JSON view starts a layer with when its file does not exist yet. */
export function newRulesFile(schemaUrl: string): string {
  return `// Itervox rules (JSONC: comments and trailing commas are fine).\n{\n  "$schema": "${schemaUrl}",\n  "rules": [],\n}\n`;
}

/** "a, b" → ["a", "b"]; blank → undefined (the scope list is omitted). */
export function splitList(value: string): string[] | undefined {
  const items = value
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s !== '');
  return items.length > 0 ? items : undefined;
}

/** Per-row problems, or null when every rule is valid and ids are unique. */
export function ruleErrors(rules: readonly Rule[]): (string | undefined)[] | null {
  const seen = new Map<string, number>();
  const errors = rules.map((rule, i) => {
    const parsed = RuleSchema.safeParse(rule);
    if (!parsed.success) {
      const issue = parsed.error.issues[0];
      return `${issue.path.map(String).join('.') || 'rule'}: ${issue.message}`;
    }
    const prev = seen.get(rule.id.toLowerCase());
    seen.set(rule.id.toLowerCase(), i);
    if (prev !== undefined) {
      return `id: "${rule.id}" is already used by rule ${String(prev + 1)}`;
    }
    return undefined;
  });
  return errors.some((e) => e !== undefined) ? errors : null;
}

let rowSeq = 0;

/** A form row: the rule plus a key that stays put while its id is edited. */
export interface RuleRow {
  key: string;
  rule: Rule;
}

export function ruleRow(rule: Rule): RuleRow {
  rowSeq += 1;
  return { key: `rule-row-${String(rowSeq)}`, rule };
}
