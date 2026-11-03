// #218 — the issue's playbook picker: Automatic / None / each playbook on
// disk, a summary of what the run gets and why, operator scope to change it.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { authedFetch } from '../../../auth/authedFetch';
import { render } from '../../../test/render';
import { makeSnapshot } from '../../../test/fixtures/snapshots';
import type { TrackerIssue } from '../../../types/schemas';
import { IssuePlaybookPicker } from '../IssuePlaybookPicker';
import IssueCard from '../IssueCard';
import { routeFetch } from '../changes/__tests__/fetchMock';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
const mockFetch = vi.mocked(authedFetch);

const playbooks = {
  enabled: true,
  playbooks: [
    {
      name: 'bug-fix',
      description: 'Reproduce first.',
      keywords: ['bug'],
      requireEvidence: ['test'],
    },
    { name: 'docs', description: 'Write for the reader.', keywords: ['docs'], requireEvidence: [] },
  ],
};

const issue = (over: Partial<TrackerIssue> = {}): TrackerIssue =>
  ({
    identifier: 'ENG-1',
    title: 'Crash on save',
    state: 'Todo',
    orchestratorState: 'idle',
    labels: ['bug'],
    playbook: 'bug-fix',
    playbookSource: 'label',
    ...over,
  }) as TrackerIssue;

beforeEach(() => {
  mockFetch.mockReset();
  routeFetch(mockFetch, {
    '/api/v1/playbooks': { body: playbooks },
    '/api/v1/issues/ENG-1/playbook': {
      status: 202,
      body: { identifier: 'ENG-1', playbook: 'docs' },
    },
  });
});

describe('IssuePlaybookPicker (#218)', () => {
  it('lists Automatic, None and every playbook and says what the run gets', async () => {
    render(<IssuePlaybookPicker issue={issue()} />);
    const select = await screen.findByRole('combobox', { name: 'Playbook' });
    const options = within(select)
      .getAllByRole('option')
      .map((o) => o.textContent);
    expect(options).toEqual(['Automatic', 'None', 'bug-fix', 'docs']);
    expect(select).toHaveValue('');
    expect(screen.getByTestId('issue-playbook-summary')).toHaveTextContent(
      'Runs with the bug-fix playbook (from a label).',
    );
  });

  it('posts the override and shows it optimistically', async () => {
    render(<IssuePlaybookPicker issue={issue()} />);
    const select = await screen.findByRole('combobox', { name: 'Playbook' });
    fireEvent.change(select, { target: { value: 'docs' } });
    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/issues/ENG-1/playbook',
        expect.objectContaining({ method: 'POST', body: JSON.stringify({ playbook: 'docs' }) }),
      );
    });
  });

  it('summarises an explicit "none" and an issue nothing matched', async () => {
    const { unmount } = render(
      <IssuePlaybookPicker issue={issue({ playbookOverride: 'none', playbook: undefined })} />,
    );
    expect(await screen.findByTestId('issue-playbook-summary')).toHaveTextContent(
      'No playbook (chosen here).',
    );
    expect(screen.getByRole('combobox', { name: 'Playbook' })).toHaveValue('none');
    unmount();
    render(
      <IssuePlaybookPicker issue={issue({ playbook: undefined, playbookSource: undefined })} />,
    );
    expect(await screen.findByTestId('issue-playbook-summary')).toHaveTextContent(
      'No playbook matched',
    );
  });

  it('shows a viewer the summary without the picker', async () => {
    render(<IssuePlaybookPicker issue={issue()} />, {
      snapshot: makeSnapshot({ callerScope: 'viewer' }),
    });
    expect(await screen.findByTestId('issue-playbook-summary')).toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: 'Playbook' })).not.toBeInTheDocument();
  });

  it('renders nothing when the daemon has no playbooks', async () => {
    routeFetch(mockFetch, { '/api/v1/playbooks': { body: { enabled: true, playbooks: [] } } });
    render(<IssuePlaybookPicker issue={issue()} />);
    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalled();
    });
    expect(screen.queryByTestId('issue-playbook')).not.toBeInTheDocument();
  });

  it('shows the playbook on the issue card', () => {
    render(<IssueCard issue={issue()} onSelect={vi.fn()} />);
    expect(screen.getByTestId('issue-card-playbook-badge')).toHaveTextContent('bug-fix');
  });
});
