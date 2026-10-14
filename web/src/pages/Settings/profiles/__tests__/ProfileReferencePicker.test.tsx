import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ProfileReferencePicker } from '../ProfileReferencePicker';

// #123: the profile's skills/agents picker lists only what the backend can use.

vi.mock('../../../../queries/skills', () => ({
  useSkillsInventory: () => ({
    data: {
      ScanTime: '2026-10-14T00:00:00Z',
      Skills: [
        { Name: 'tdd', Provider: 'claude', Source: 'project', ApproxTokens: 1 },
        { Name: 'codex-lint', Provider: 'codex', Source: 'user-codex', ApproxTokens: 1 },
        { Name: 'shared-docs', Provider: 'shared', Source: 'user', ApproxTokens: 1 },
      ],
      Subagents: [
        { Name: 'code-reviewer', Provider: 'claude', Source: 'project', ApproxTokens: 1 },
      ],
      Prompts: [{ Name: 'review', Provider: 'codex', Source: 'user-codex', ApproxTokens: 1 }],
      Plugins: [
        {
          Name: 'cx',
          Provider: 'codex',
          Source: 'user-codex',
          ApproxTokens: 1,
          Skills: [{ Name: 'cx-skill', Provider: 'codex', Source: 'user-codex', ApproxTokens: 1 }],
          Agents: [{ Name: 'cx-agent' }],
        },
      ],
    },
  }),
}));

const names = (backend: string) =>
  within(screen.getByRole('region', { name: `Skills and agents for ${backend}` }))
    .getAllByRole('button')
    .map((b) => b.getAttribute('aria-label'));

describe('ProfileReferencePicker (#123)', () => {
  it('picker shows Claude skills and subagents for a Claude profile', () => {
    render(<ProfileReferencePicker backend="claude" onInsert={vi.fn()} />);
    expect(names('Claude')).toEqual([
      'Insert skill tdd',
      'Insert skill shared-docs',
      'Insert subagent code-reviewer',
    ]);
    expect(screen.getByText(/2 skills, 1 subagent\./)).toBeInTheDocument();
  });

  it('picker shows Codex skills and prompts, and no subagents, for a Codex profile', () => {
    render(<ProfileReferencePicker backend="codex" onInsert={vi.fn()} />);
    expect(names('Codex')).toEqual([
      'Insert skill codex-lint',
      'Insert skill shared-docs',
      'Insert skill cx-skill',
      'Insert prompt review',
    ]);
  });

  it('inserts a reference the doctor check recognises', () => {
    const onInsert = vi.fn();
    render(<ProfileReferencePicker backend="claude" onInsert={onInsert} />);
    fireEvent.click(screen.getByRole('button', { name: 'Insert skill tdd' }));
    fireEvent.click(screen.getByRole('button', { name: 'Insert subagent code-reviewer' }));
    expect(onInsert.mock.calls).toEqual([['Use the `tdd` skill.'], ['@agent-code-reviewer']]);
  });
});
