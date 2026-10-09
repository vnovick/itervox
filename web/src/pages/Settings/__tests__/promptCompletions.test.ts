import { describe, expect, it } from 'vitest';
import {
  LIQUID_FILTERS,
  PROFILE_VARIABLES,
  TRIGGER_VARIABLES,
  applyCompletion,
  candidatesFor,
  completionContext,
  matchCompletions,
  referenceCompletions,
  type Completion,
} from '../profiles/promptCompletions';
import type { SkillsInventory } from '../../../types/schemas';

const at = (text: string) => completionContext(text, text.length);

describe('completionContext (#87)', () => {
  it('opens a reference after / or @ at a word start', () => {
    expect(at('Use /td')).toEqual({ kind: 'reference', query: 'td', start: 5, trigger: '/' });
    expect(at('@')).toEqual({ kind: 'reference', query: '', start: 1, trigger: '@' });
    expect(at('ask (@rev')).toMatchObject({ kind: 'reference', query: 'rev', trigger: '@' });
  });

  it('does not open inside a path, URL or email', () => {
    expect(at('see docs/rea')).toBeNull();
    expect(at('https://exa')).toBeNull();
    expect(at('me@example')).toBeNull();
    expect(at('Use /tdd now')).toBeNull();
  });

  it('opens a variable after {{ and a filter after |', () => {
    expect(at('Title: {{ iss')).toEqual({ kind: 'variable', query: 'iss', start: 10 });
    expect(at('{{')).toEqual({ kind: 'variable', query: '', start: 2 });
    expect(at('{{ issue.title | down')).toEqual({ kind: 'filter', query: 'down', start: 17 });
  });

  it('closes after }}, across lines, and inside filter arguments', () => {
    expect(at('{{ issue.title }} and')).toBeNull();
    expect(at('{{ issue\n.title')).toBeNull();
    expect(at('{{ issue.title | default: "x')).toBeNull();
  });
});

const inventory = {
  ScanTime: '2026-10-09T00:00:00Z',
  Skills: [
    {
      Name: 'tdd',
      Description: 'Test first.\nMore.',
      Provider: 'claude',
      Source: 'project',
      ApproxTokens: 1,
    },
    { Name: 'deploy', Provider: 'claude', Source: 'user', ApproxTokens: 1 },
  ],
  Subagents: [
    {
      Name: 'code-reviewer',
      Description: 'Reviews diffs',
      Provider: 'claude',
      Source: 'project',
      ApproxTokens: 1,
    },
  ],
  Plugins: [
    {
      Name: 'superpowers',
      Provider: 'claude',
      Source: 'marketplace',
      ApproxTokens: 1,
      Skills: [{ Name: 'tdd', Provider: 'claude', Source: 'plugin:superpowers', ApproxTokens: 1 }],
      Agents: [{ Name: 'planner', Description: 'Plans work' }],
    },
  ],
} as SkillsInventory;

describe('referenceCompletions (#87)', () => {
  it('lists skills and subagents with a one-line description and source, once each', () => {
    const refs = referenceCompletions(inventory);
    expect(refs).toEqual([
      { label: 'tdd', kind: 'skill', detail: 'Test first.', source: 'project' },
      { label: 'deploy', kind: 'skill', detail: undefined, source: 'user' },
      { label: 'code-reviewer', kind: 'subagent', detail: 'Reviews diffs', source: 'project' },
      { label: 'planner', kind: 'subagent', detail: 'Plans work', source: 'plugin:superpowers' },
    ]);
    expect(referenceCompletions(null)).toEqual([]);
  });
});

describe('matchCompletions (#87)', () => {
  it('puts prefix matches before substring matches and limits the list', () => {
    const items: Completion[] = ['run.timestamp', 'issue.state', 'issue.title'].map((label) => ({
      label,
      kind: 'variable',
    }));
    expect(matchCompletions(items, 'iss').map((c) => c.label)).toEqual([
      'issue.state',
      'issue.title',
    ]);
    expect(matchCompletions(items, 'TIME').map((c) => c.label)).toEqual(['run.timestamp']);
    expect(matchCompletions(items, '', 2)).toHaveLength(2);
  });

  it('chooses candidates by context', () => {
    const refs = referenceCompletions(inventory);
    expect(candidatesFor({ kind: 'reference', query: '', start: 0 }, refs, PROFILE_VARIABLES)).toBe(
      refs,
    );
    expect(candidatesFor({ kind: 'variable', query: '', start: 0 }, refs, PROFILE_VARIABLES)).toBe(
      PROFILE_VARIABLES,
    );
    expect(candidatesFor({ kind: 'filter', query: '', start: 0 }, refs, PROFILE_VARIABLES)).toBe(
      LIQUID_FILTERS,
    );
  });
});

describe('applyCompletion (#87)', () => {
  const insert = (text: string, label: string, kind: Completion['kind']) => {
    const ctx = at(text);
    if (!ctx) throw new Error('no context');
    return applyCompletion(text, text.length, ctx, { label, kind });
  };

  it('inserts a skill or subagent name after its trigger', () => {
    expect(insert('Use /td', 'tdd', 'skill')).toEqual({ text: 'Use /tdd ', caret: 9 });
    expect(insert('Ask @', 'code-reviewer', 'subagent').text).toBe('Ask @code-reviewer ');
  });

  it('completes a variable and closes the tag', () => {
    expect(insert('{{', 'issue.title', 'variable')).toEqual({
      text: '{{ issue.title }}',
      caret: 17,
    });
    expect(insert('{{ iss', 'issue.title', 'variable').text).toBe('{{ issue.title }}');
  });

  it('does not add a second closing tag', () => {
    const text = '{{ iss }} rest';
    const ctx = completionContext(text, 6);
    if (!ctx) throw new Error('no context');
    expect(applyCompletion(text, 6, ctx, { label: 'issue.title', kind: 'variable' }).text).toBe(
      '{{ issue.title }} rest',
    );
  });

  it('completes a filter', () => {
    expect(insert('{{ issue.title |', 'downcase', 'filter').text).toBe('{{ issue.title | downcase');
  });
});

describe('variable lists (#87)', () => {
  it('match what the daemon binds for profile prompts and automations', () => {
    const labels = PROFILE_VARIABLES.map((c) => c.label);
    for (const v of [
      'issue.title',
      'issue.identifier',
      'attempt',
      'run.handoff_path',
      'run.pr_base_branch',
    ]) {
      expect(labels).toContain(v);
    }
    expect(TRIGGER_VARIABLES.every((c) => c.label.startsWith('trigger.'))).toBe(true);
  });
});
