import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { ContextBudget } from '../ContextBudget';
import { ProviderBreakdown } from '../ProviderBreakdown';
import type { SkillsInventory } from '../../../../types/schemas';

const skill = (Name: string, Provider: string, Source: string, ApproxTokens: number) => ({
  Name,
  Description: '',
  Provider,
  Source,
  FilePath: `/skills/${Name}/SKILL.md`,
  ApproxTokens,
  TriggerPatterns: [],
});

const inventory = {
  ScanTime: '2026-05-07T10:00:00Z',
  Skills: [
    skill('deploy', 'claude', 'project', 400),
    skill('graphify', 'codex', 'user-codex', 200),
    skill('notes', 'claude', 'shared-agents', 100),
  ],
} as unknown as SkillsInventory;

describe('Repo bars are coloured per series', () => {
  it('gives claude, shared and codex their own series fill', () => {
    render(<ProviderBreakdown inventory={inventory} />);
    expect(screen.getByTestId('provider-bar-claude')).toHaveClass('bg-theme-series-1');
    expect(screen.getByTestId('provider-bar-shared')).toHaveClass('bg-theme-series-2');
    expect(screen.getByTestId('provider-bar-codex')).toHaveClass('bg-theme-series-3');
    expect(screen.getByTestId('provider-bar-claude')).toHaveStyle({ width: '100%' });
    expect(screen.getByTestId('provider-bar-codex')).toHaveStyle({ width: '50%' });
  });

  it('colours each context-budget kind with a distinct series', () => {
    render(
      <ContextBudget
        tokens={{ skills: 400, subagents: 10, plugins: 0, mcp: 800, hooks: 6, instructions: 200 }}
      />,
    );
    const fills = ['skills', 'subagents', 'plugins', 'mcp', 'hooks', 'instructions'].map((k) =>
      [...screen.getByTestId(`budget-bar-${k}`).classList].find((c) => c.startsWith('bg-')),
    );
    expect(new Set(fills).size).toBe(6);
    expect(fills.every((c) => c?.startsWith('bg-theme-series-') === true)).toBe(true);
  });
});
