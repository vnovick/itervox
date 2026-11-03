import { describe, expect, it } from 'vitest';
import { layerLabel, newRulesFile, ruleErrors, ruleRow, splitList } from '../rulesModel';
import { parseRulesText } from '../../../lib/rulesText';

describe('rulesModel', () => {
  it('splits comma lists and drops blanks', () => {
    expect(splitList(' a, b ,, ')).toEqual(['a', 'b']);
    expect(splitList(' , ')).toBeUndefined();
  });

  it('reports per-row problems and duplicate ids', () => {
    expect(ruleErrors([{ id: 'a', severity: 'must', text: 'A.' }])).toBeNull();
    const errs = ruleErrors([
      { id: 'a', severity: 'must', text: 'A.' },
      { id: 'A', severity: 'should', text: 'B.' },
      { id: '', severity: 'should', text: 'C.' },
    ]);
    expect(errs?.[0]).toBeUndefined();
    expect(errs?.[1]).toContain('already used by rule 1');
    expect(errs?.[2]).toContain('id');
  });

  it('gives each row its own key', () => {
    const rule = { id: 'a', severity: 'must' as const, text: 'A.' };
    expect(ruleRow(rule).key).not.toBe(ruleRow(rule).key);
  });

  it('labels layers and starts a valid empty file', () => {
    expect(layerLabel('local')).toBe('Project local');
    expect(layerLabel('org')).toBe('org');
    expect(parseRulesText(newRulesFile('https://x/schema.json'))).toEqual({ ok: true, rules: [] });
  });
});
