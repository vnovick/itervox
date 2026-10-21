import { describe, expect, it } from 'vitest';
import {
  appendReference,
  backendItems,
  describeCounts,
  guessProfileBackend,
  itemCounts,
  providerFits,
  referenceText,
} from '../backendInventory';
import { referenceCompletions } from '../promptCompletions';
import type { SkillsInventory } from '../../../../types/schemas';

const inv: SkillsInventory = {
  ScanTime: '2026-10-14T00:00:00Z',
  Skills: [
    { Name: 'tdd', Provider: 'claude', Source: 'project', ApproxTokens: 1 },
    { Name: 'codex-lint', Provider: 'codex', Source: 'user-codex', ApproxTokens: 1 },
    { Name: 'both', Provider: 'shared', Source: 'user', ApproxTokens: 1 },
  ],
  Subagents: [
    { Name: 'code-reviewer', Provider: 'claude', Source: 'project', ApproxTokens: 1 },
    { Name: 'helper', Provider: 'claude', Source: 'plugin:p', ApproxTokens: 1 },
  ],
  Prompts: [{ Name: 'review', Provider: 'codex', Source: 'user-codex', ApproxTokens: 1 }],
  Plugins: [
    {
      Name: 'p',
      Provider: 'claude',
      Source: 'user',
      ApproxTokens: 1,
      Skills: [{ Name: 'p-skill', Provider: 'claude', Source: 'plugin:p', ApproxTokens: 1 }],
      Agents: [{ Name: 'helper' }],
    },
  ],
};

const keys = (backend?: 'claude' | 'codex') =>
  backendItems(inv, backend).map((i) => `${i.kind}:${i.name}`);

describe('backendItems (#123)', () => {
  it('Codex profile is not offered Claude subagents', () => {
    expect(keys('codex')).toEqual(['skill:codex-lint', 'skill:both', 'prompt:review']);
    expect(referenceCompletions(inv, 'codex').map((c) => c.label)).toEqual([
      'codex-lint',
      'both',
      'prompts:review',
    ]);
  });

  it('Claude profile is offered Claude subagents', () => {
    expect(keys('claude')).toEqual([
      'skill:tdd',
      'skill:both',
      'skill:p-skill',
      'subagent:code-reviewer',
      'subagent:helper',
    ]);
  });

  it('lists everything without a backend', () => {
    expect(keys()).toHaveLength(7);
  });

  it('treats shared and empty providers as both backends', () => {
    expect(providerFits('shared', 'codex')).toBe(true);
    expect(providerFits('', 'claude')).toBe(true);
    expect(providerFits(undefined, 'codex')).toBe(true);
    expect(providerFits('claude', 'codex')).toBe(false);
  });

  it('guesses a draft backend the way the runner picks it', () => {
    expect(guessProfileBackend('codex --model x', 'claude')).toBe('codex');
    expect(guessProfileBackend('run.sh', 'codex')).toBe('codex');
    expect(guessProfileBackend('', '', 'codex')).toBe('codex');
    expect(guessProfileBackend(undefined, undefined)).toBe('claude');
  });

  it('formats references, counts and appends', () => {
    expect(referenceText({ kind: 'prompt', name: 'review' })).toBe('/prompts:review');
    expect(describeCounts(itemCounts(backendItems(inv, 'codex')))).toBe('2 skills, 1 prompt');
    expect(describeCounts(itemCounts([]))).toBe('no skills found');
    expect(appendReference('Text\n\n', '@agent-x')).toBe('Text\n\n@agent-x\n');
    expect(appendReference('', '@agent-x')).toBe('@agent-x\n');
    expect(backendItems(null, 'claude')).toEqual([]);
  });
});
