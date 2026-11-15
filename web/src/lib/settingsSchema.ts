// Settings page model (#158): the settings JSON Schema (generated from the Go
// config registry and served at /api/v1/settings/schema) flattened into field
// definitions, plus the parsing the generic field renderers and the JSON view
// need. A setting added to the registry shows up here with no form code.

import type { SettingsSchemaDoc, SettingsSchemaNode } from '../types/schemas';
import { stripJsonc } from './rulesText';

/** How a field is edited. `json` fields are shown read-only (edited as JSON). */
export type SettingsFieldKind =
  | 'boolean'
  | 'integer'
  | 'number'
  | 'enum'
  | 'string'
  | 'list'
  | 'map'
  | 'json';

export interface SettingsFieldDef {
  key: string;
  section: string;
  kind: SettingsFieldKind;
  label: string;
  description: string;
  group: string;
  default?: unknown;
  enum: string[];
  minimum?: number;
  /** Element type of a list, value type of a map. */
  itemType?: string;
  live: boolean;
  /** The page that edits the value instead of this one. */
  editor?: string;
  replacedBy?: string;
}

function kindOf(node: SettingsSchemaNode): SettingsFieldKind {
  if (node['x-itervox-editor']) return 'json';
  if (node.enum && node.enum.length > 0) return 'enum';
  switch (node.type) {
    case 'boolean':
      return 'boolean';
    case 'integer':
      return 'integer';
    case 'number':
      return 'number';
    case 'string':
      return 'string';
    case 'array':
      return 'list';
    case 'object':
      return typeof node.additionalProperties === 'object' ? 'map' : 'json';
  }
  return 'json';
}

function isContainer(node: SettingsSchemaNode): boolean {
  return node.type === 'object' && !!node.properties && !node['x-itervox-group'];
}

/** Every setting in the schema, sections and keys in schema order. */
export function flattenSettingsSchema(doc: SettingsSchemaDoc): SettingsFieldDef[] {
  const out: SettingsFieldDef[] = [];
  const walk = (section: string, prefix: string, node: SettingsSchemaNode) => {
    for (const [name, child] of Object.entries(node.properties ?? {})) {
      const key = `${prefix}.${name}`;
      if (isContainer(child)) {
        walk(section, key, child);
        continue;
      }
      const additional = child.additionalProperties;
      out.push({
        key,
        section,
        kind: kindOf(child),
        label: child.title ?? key,
        description: child.description ?? '',
        group: child['x-itervox-group'] ?? section,
        default: child.default,
        enum: child.enum ?? [],
        minimum: child.minimum,
        itemType:
          child.items?.type ?? (typeof additional === 'object' ? additional.type : undefined),
        live: child['x-itervox-live'] === true,
        editor: child['x-itervox-editor'],
        replacedBy: child['x-itervox-replaced-by'],
      });
    }
  };
  for (const [section, node] of Object.entries(doc.properties)) {
    if (section.startsWith('$')) continue;
    if (node.type !== 'object') {
      // A list section (#239: automations) is itself the setting.
      out.push({
        key: section,
        section,
        kind: kindOf(node),
        label: node.title ?? section,
        description: node.description ?? '',
        group: node['x-itervox-group'] ?? section,
        default: node.default,
        enum: node.enum ?? [],
        minimum: node.minimum,
        itemType: node.items?.type,
        live: node['x-itervox-live'] === true,
        editor: node['x-itervox-editor'],
        replacedBy: node['x-itervox-replaced-by'],
      });
      continue;
    }
    walk(section, section, node);
  }
  return out;
}

/** Fields grouped by their heading, groups in first-seen order. */
export function groupSettingsFields(fields: SettingsFieldDef[]): [string, SettingsFieldDef[]][] {
  const groups = new Map<string, SettingsFieldDef[]>();
  for (const f of fields) {
    const list = groups.get(f.group) ?? [];
    list.push(f);
    groups.set(f.group, list);
  }
  return [...groups.entries()];
}

/** The text a list or map field shows: one entry per line. */
export function fieldText(def: SettingsFieldDef, value: unknown): string {
  if (value === undefined || value === null) return '';
  if (def.kind === 'list' && Array.isArray(value)) return value.map(String).join('\n');
  if (def.kind === 'map' && typeof value === 'object') {
    return Object.entries(value as Record<string, unknown>)
      .map(([k, v]) => `${k} = ${show(v)}`)
      .join('\n');
  }
  if (def.kind === 'json') return JSON.stringify(value, null, 2);
  return show(value);
}

function show(v: unknown): string {
  if (typeof v === 'string') return v;
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  return JSON.stringify(v);
}

export type ParsedField = { ok: true; value: unknown } | { ok: false; error: string };

function scalar(type: string | undefined, text: string): ParsedField {
  if (type === 'integer' || type === 'number') {
    const n = Number(text);
    if (text.trim() === '' || Number.isNaN(n))
      return { ok: false, error: `"${text}" is not a number` };
    if (type === 'integer' && !Number.isInteger(n))
      return { ok: false, error: `"${text}" is not a whole number` };
    return { ok: true, value: n };
  }
  return { ok: true, value: text };
}

/** Parses what the operator typed into a field into the value it saves. */
export function parseFieldInput(def: SettingsFieldDef, text: string): ParsedField {
  switch (def.kind) {
    case 'integer':
    case 'number': {
      const r = scalar(def.kind, text);
      if (r.ok && def.minimum !== undefined && (r.value as number) < def.minimum)
        return { ok: false, error: `must be at least ${String(def.minimum)}` };
      return r;
    }
    case 'list':
      return {
        ok: true,
        value: text
          .split('\n')
          .map((l) => l.trim())
          .filter(Boolean),
      };
    case 'map': {
      const out: Record<string, unknown> = {};
      for (const line of text.split('\n').filter((l) => l.trim() !== '')) {
        const at = line.indexOf('=');
        if (at < 1) return { ok: false, error: `"${line}" is not key = value` };
        const v = scalar(def.itemType, line.slice(at + 1).trim());
        if (!v.ok) return v;
        out[line.slice(0, at).trim()] = v.value;
      }
      return { ok: true, value: out };
    }
    default:
      return { ok: true, value: text };
  }
}

export type ParsedSettingsText = { ok: true; keys: number } | { ok: false; error: string };

function typeMatches(def: SettingsFieldDef, v: unknown): boolean {
  switch (def.kind) {
    case 'boolean':
      return typeof v === 'boolean';
    case 'integer':
      return Number.isInteger(v);
    case 'number':
      return typeof v === 'number';
    case 'enum':
    case 'string':
      return typeof v === 'string' && (def.enum.length === 0 || def.enum.includes(v));
    case 'list':
      return Array.isArray(v);
    default:
      // A json field whose items are objects (#239: automations,
      // tracker.filter) holds a list of them.
      if (def.itemType === 'object') return Array.isArray(v);
      return typeof v === 'object' && v !== null && !Array.isArray(v);
  }
}

/** Checks the JSON view's text the way the daemon will before it saves it. */
export function parseSettingsText(text: string, fields: SettingsFieldDef[]): ParsedSettingsText {
  let data: unknown;
  try {
    data = JSON.parse(stripJsonc(text)) as unknown;
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : 'not valid JSON' };
  }
  if (typeof data !== 'object' || data === null || Array.isArray(data))
    return { ok: false, error: 'the file must be one JSON object' };
  const sections = new Set(fields.map((f) => f.section));
  // A list section (#239: automations) is one field; its type check is below.
  const listSections = new Set(fields.filter((f) => f.key === f.section).map((f) => f.section));
  let keys = 0;
  for (const [section, value] of Object.entries(data as Record<string, unknown>)) {
    if (section === '$schema') continue;
    if (!sections.has(section))
      return {
        ok: false,
        error: `"${section}" cannot be set here (sections: ${[...sections].join(', ')})`,
      };
    if (listSections.has(section)) continue;
    if (typeof value !== 'object' || value === null || Array.isArray(value))
      return { ok: false, error: `"${section}" must be an object of settings` };
  }
  for (const f of fields) {
    let cur: unknown = data;
    for (const part of f.key.split('.')) {
      cur =
        typeof cur === 'object' && cur !== null
          ? (cur as Record<string, unknown>)[part]
          : undefined;
    }
    if (cur === undefined) continue;
    if (!typeMatches(f, cur))
      return {
        ok: false,
        error: `${f.key} must be a ${f.kind}${f.enum.length ? ` (${f.enum.join(', ')})` : ''}`,
      };
    keys++;
  }
  return { ok: true, keys };
}

/** A new settings file for the JSON view of a layer that does not exist yet. */
export function newSettingsFile(schemaUrl: string): string {
  return `// Itervox settings (JSONC: comments and trailing commas are fine).\n{\n  "$schema": "${schemaUrl}",\n}\n`;
}

/** The Settings page's name and git policy for each writable layer. */
export const SETTINGS_LAYER_INFO: Partial<Record<string, { label: string; git: string }>> = {
  user: {
    label: 'User',
    git: 'Yours, on this machine: applies to every project. Never committed.',
  },
  project: { label: 'Project', git: 'Committed with the code: the team’s settings.' },
  local: { label: 'Project local', git: 'Yours, for this project only. Gitignored.' },
  workflow: {
    label: 'WORKFLOW.md',
    git: 'The workflow’s front matter (deprecated for these settings): wins over your user file; the project and local files win over it.',
  },
  default: { label: 'Default', git: 'Built in.' },
  environment: { label: 'Environment', git: 'An environment variable.' },
};

export function settingsLayerLabel(name: string): string {
  return SETTINGS_LAYER_INFO[name]?.label ?? name;
}
