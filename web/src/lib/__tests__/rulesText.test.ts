import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { parseRulesText, stripJsonc } from '../rulesText';
import { RULE_ID_PATTERN } from '../../types/schemas';

describe('stripJsonc', () => {
  it('drops comments and trailing commas but not look-alikes inside strings', () => {
    const text = `// header
{
  /* block */ "a": "http://x.dev/y, }", // tail
  "b": [1, 2,],
}`;
    expect(JSON.parse(stripJsonc(text))).toEqual({ a: 'http://x.dev/y, }', b: [1, 2] });
  });

  it('keeps escaped quotes inside strings', () => {
    expect(JSON.parse(stripJsonc('{"a": "say \\"hi\\" // not a comment",}'))).toEqual({
      a: 'say "hi" // not a comment',
    });
  });
});

describe('parseRulesText (#157 JSON view validation)', () => {
  it('accepts a JSONC rules file', () => {
    const r = parseRulesText(`// mine
{ "$schema": "x", "rules": [
  {"id": "pnpm", "severity": "must", "text": "Use pnpm.", "scope": {"backends": ["claude"]}},
] }`);
    expect(r).toEqual({
      ok: true,
      rules: [{ id: 'pnpm', severity: 'must', text: 'Use pnpm.', scope: { backends: ['claude'] } }],
    });
  });

  it('treats blank text as no rules', () => {
    expect(parseRulesText('  ')).toEqual({ ok: true, rules: [] });
  });

  it.each([
    ['{"rules": [', 'Not valid JSON'],
    ['{"rules": [{"id": "a", "text": "t"}]}', 'rules.0.severity'],
    ['{"rules": [{"id": "a", "severity": "may", "text": "t"}]}', 'rules.0.severity'],
    ['{"rules": [{"id": "has space", "severity": "must", "text": "t"}]}', 'rules.0.id'],
    ['{"rules": [{"id": "a", "severity": "must", "text": "t", "level": 1}]}', 'rules.0'],
    ['{"rule": []}', 'file'],
    [
      '{"rules": [{"id": "a", "severity": "must", "text": "t"}, {"id": "A", "severity": "should", "text": "u"}]}',
      'already used',
    ],
  ])('rejects %s', (text, fragment) => {
    const r = parseRulesText(text);
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.error).toContain(fragment);
  });
});

// The Zod mirror and the published JSON Schema must agree (the Go side has
// TestPublishedRulesSchemaMatchesParser).
describe('published rules schema parity', () => {
  it('uses the same id pattern and severities as RuleSchema', () => {
    const raw: string = readFileSync(
      resolve(process.cwd(), '../site/public/schema/rules.schema.json'),
      'utf8',
    );
    const schema = JSON.parse(raw) as {
      $defs: { rule: { properties: { id: { pattern: string }; severity: { enum: string[] } } } };
    };
    expect(schema.$defs.rule.properties.id.pattern).toBe(RULE_ID_PATTERN.source);
    expect(schema.$defs.rule.properties.severity.enum).toEqual(['must', 'should']);
  });
});
