import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  fieldText,
  flattenSettingsSchema,
  groupSettingsFields,
  newSettingsFile,
  parseFieldInput,
  parseSettingsText,
  settingsLayerLabel,
  type SettingsFieldDef,
} from '../settingsSchema';
import { SettingsSchemaDocSchema } from '../../types/schemas';

// The published schema is generated from the Go config registry
// (`make settings-schema`); the page renders whatever it lists.
function publishedFields(): SettingsFieldDef[] {
  const raw: string = readFileSync(
    resolve(process.cwd(), '../site/public/schema/settings.schema.json'),
    'utf8',
  );
  return flattenSettingsSchema(SettingsSchemaDocSchema.parse(JSON.parse(raw)));
}

const byKey = (fields: SettingsFieldDef[], key: string) => {
  const f = fields.find((x) => x.key === key);
  if (!f) throw new Error(`no field ${key}`);
  return f;
};

describe('flattenSettingsSchema (#158)', () => {
  it('turns the published schema into one field per layered setting', () => {
    const fields = publishedFields();
    expect(new Set(fields.map((f) => f.section))).toEqual(
      new Set([
        'agent',
        'automations',
        'dependencies',
        'hooks',
        'hub',
        'polling',
        'server',
        'tracker',
        'workspace',
      ]),
    );
    // #239: the list section is one read-only field the Automations page edits.
    expect(byKey(fields, 'automations')).toMatchObject({
      kind: 'json',
      editor: 'automations',
      itemType: 'object',
    });
    expect(byKey(fields, 'tracker.active_states')).toMatchObject({
      kind: 'list',
      itemType: 'string',
      live: true,
    });
    expect(byKey(fields, 'dependencies.ordering').kind).toBe('enum');
    expect(byKey(fields, 'hooks.timeout_ms')).toMatchObject({ kind: 'integer', default: 60000 });
    const workers = byKey(fields, 'agent.max_concurrent_agents');
    expect(workers).toMatchObject({ kind: 'integer', live: true, minimum: 1, default: 10 });
    expect(byKey(fields, 'agent.budget.tokens').kind).toBe('integer');
    expect(byKey(fields, 'agent.dispatch_strategy')).toMatchObject({
      kind: 'enum',
      enum: ['round-robin', 'least-loaded'],
    });
    expect(byKey(fields, 'agent.ssh_hosts')).toMatchObject({ kind: 'list', itemType: 'string' });
    expect(byKey(fields, 'agent.max_concurrent_agents_by_state')).toMatchObject({
      kind: 'map',
      itemType: 'integer',
    });
    expect(byKey(fields, 'agent.profiles')).toMatchObject({ kind: 'json', editor: 'profiles' });
    expect(byKey(fields, 'agent.backend_fallback').kind).toBe('json');
    expect(byKey(fields, 'server.allow_unauthenticated_lan').replacedBy).toBe(
      'server.allow_unauthenticated',
    );
    expect(groupSettingsFields(fields).map(([g]) => g)).toContain('Capacity');
  });
});

describe('field input parsing (#158)', () => {
  const fields = publishedFields();
  it('parses numbers with their minimum', () => {
    const f = byKey(fields, 'agent.max_turns');
    expect(parseFieldInput(f, '12')).toEqual({ ok: true, value: 12 });
    expect(parseFieldInput(f, '1.5')).toMatchObject({ ok: false });
    expect(parseFieldInput(f, 'x')).toMatchObject({ ok: false });
    expect(parseFieldInput(f, '0')).toEqual({ ok: false, error: 'must be at least 1' });
    expect(parseFieldInput(byKey(fields, 'agent.budget.cost_usd'), '2.5')).toEqual({
      ok: true,
      value: 2.5,
    });
  });

  it('parses one-per-line lists and key = value maps', () => {
    const list = byKey(fields, 'agent.ssh_hosts');
    expect(parseFieldInput(list, 'a\n\n b \n')).toEqual({ ok: true, value: ['a', 'b'] });
    expect(fieldText(list, ['a', 'b'])).toBe('a\nb');
    const map = byKey(fields, 'agent.max_concurrent_agents_by_state');
    expect(parseFieldInput(map, 'todo = 2\nin progress=1')).toEqual({
      ok: true,
      value: { todo: 2, 'in progress': 1 },
    });
    expect(parseFieldInput(map, 'todo')).toMatchObject({ ok: false });
    expect(parseFieldInput(map, 'todo = many')).toMatchObject({ ok: false });
    expect(fieldText(map, { todo: 2 })).toBe('todo = 2');
    expect(fieldText(byKey(fields, 'agent.profiles'), { a: 1 })).toContain('"a": 1');
    expect(parseFieldInput(byKey(fields, 'agent.command'), 'codex')).toEqual({
      ok: true,
      value: 'codex',
    });
  });
});

describe('parseSettingsText (#158 JSON view validation)', () => {
  const fields = publishedFields();
  it('accepts a JSONC settings file and counts the settings it sets', () => {
    expect(
      parseSettingsText(
        `// mine\n{"$schema": "x", "agent": {"max_turns": 4, "budget": {"tokens": 9},}, "polling": {}}`,
        fields,
      ),
    ).toEqual({ ok: true, keys: 2 });
    // #239: the new sections, the automations list and a filter list.
    expect(
      parseSettingsText(
        `{"tracker": {"kind": "linear", "filter": [{"labels_any": ["a"]}]}, "hooks": {}, "automations": [{"id": "x"}]}`,
        fields,
      ),
    ).toEqual({ ok: true, keys: 3 });
  });

  it.each([
    ['{"agent": ', 'JSON'],
    ['[]', 'one JSON object'],
    ['{"guardrails": {}}', 'cannot be set here'],
    ['{"automations": {"id": "x"}}', 'automations must be a json'],
    ['{"agent": 3}', 'must be an object'],
    ['{"agent": {"max_turns": "x"}}', 'agent.max_turns must be a integer'],
    ['{"agent": {"dispatch_strategy": "fastest"}}', 'round-robin'],
    ['{"agent": {"ssh_hosts": "a"}}', 'agent.ssh_hosts'],
  ])('rejects %s', (text, fragment) => {
    const r = parseSettingsText(text, fields);
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.error).toContain(fragment);
  });

  it('starts a new file with the schema URL and names layers', () => {
    expect(newSettingsFile('https://s')).toContain('"$schema": "https://s"');
    expect(settingsLayerLabel('local')).toBe('Project local');
    expect(settingsLayerLabel('other')).toBe('other');
  });
});
