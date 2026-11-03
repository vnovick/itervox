import { fireEvent, render, screen, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { IssueProfilePicker } from '../IssueProfilePicker';
import type { TrackerIssue } from '../../../types/schemas';

// #123: the issue's profile picker names each profile's backend and lists
// what that backend can use.

const mutate = vi.fn();
vi.mock('../../../queries/issues', () => ({
  useSetIssueProfile: () => ({ mutate, isPending: false }),
}));

let backends: { default: string; profiles: Record<string, string> } | undefined;
vi.mock('../../../queries/promptRefs', () => ({
  useProfileBackends: () => ({ data: backends }),
}));

vi.mock('../../../queries/skills', () => ({
  useSkillsInventory: () => ({
    data: {
      ScanTime: '2026-10-14T00:00:00Z',
      Skills: [
        { Name: 'tdd', Provider: 'claude', Source: 'project', ApproxTokens: 1 },
        { Name: 'codex-lint', Provider: 'codex', Source: 'user-codex', ApproxTokens: 1 },
      ],
      Subagents: [
        { Name: 'code-reviewer', Provider: 'claude', Source: 'project', ApproxTokens: 1 },
      ],
      Prompts: [{ Name: 'review', Provider: 'codex', Source: 'user-codex', ApproxTokens: 1 }],
    },
  }),
}));

const snapshot = {
  availableProfiles: ['worker', 'fast', 'wrapped'],
  defaultBackend: 'claude',
  profileDefs: {
    worker: { command: 'claude' },
    fast: { command: 'codex' },
    // Not in the daemon's map yet: guessed from its definition.
    wrapped: { command: 'run.sh', backend: 'codex' },
  },
};
vi.mock('../../../store/itervoxStore', () => ({
  useItervoxStore: (selector: (s: { snapshot: typeof snapshot }) => unknown) =>
    selector({ snapshot }),
}));

const issue = (over: Partial<TrackerIssue> = {}): TrackerIssue => ({
  identifier: 'ENG-1',
  title: 'Fix it',
  state: 'Todo',
  orchestratorState: 'idle',
  ...over,
});

const summary = () => screen.getByTestId('issue-profile-capabilities').textContent;

beforeEach(() => {
  mutate.mockReset();
  backends = { default: 'claude', profiles: { worker: 'claude', fast: 'codex' } };
});

describe('IssueProfilePicker (#123)', () => {
  it('picker labels every profile with the backend it runs on', () => {
    render(<IssueProfilePicker issue={issue()} locked={false} />);
    const select = screen.getByRole('combobox', { name: 'Agent profile' });
    expect(
      within(select)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['Default · Claude', 'worker · Claude', 'fast · Codex', 'wrapped · Codex']);
    fireEvent.change(select, { target: { value: 'fast' } });
    expect(mutate).toHaveBeenCalledWith({ identifier: 'ENG-1', profile: 'fast' });
  });

  it('picker shows Claude skills and subagents for a Claude profile', () => {
    render(<IssueProfilePicker issue={issue({ agentProfile: 'worker' })} locked={false} />);
    expect(summary()).toBe('Claude can use 1 skill, 1 subagent: tdd, code-reviewer.');
    expect(screen.getByRole('combobox', { name: 'Agent profile' })).toHaveAccessibleDescription(
      'Claude can use 1 skill, 1 subagent: tdd, code-reviewer.',
    );
  });

  it('picker shows Codex skills and prompts, not Claude subagents, for a Codex profile', () => {
    render(<IssueProfilePicker issue={issue({ agentProfile: 'fast' })} locked={false} />);
    expect(summary()).toBe('Codex can use 1 skill, 1 prompt: codex-lint, prompts:review.');
    expect(summary()).not.toContain('code-reviewer');
  });

  it('follows a per-issue backend pin over the profile backend', () => {
    render(
      <IssueProfilePicker
        issue={issue({ agentProfile: 'worker', agentBackend: 'codex' })}
        locked
      />,
    );
    expect(screen.queryByRole('combobox')).toBeNull();
    expect(screen.getByText(/locked while Todo/)).toBeInTheDocument();
    expect(summary()).toContain('Codex can use');
  });

  it('uses the daemon default backend for the Default option', () => {
    backends = { default: 'codex', profiles: {} };
    render(<IssueProfilePicker issue={issue()} locked={false} />);
    expect(screen.getByRole('option', { name: 'Default · Codex' })).toBeInTheDocument();
    expect(summary()).toContain('Codex can use');
  });
});
