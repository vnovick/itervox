// #251 — a New task's instructions in the issue detail: shown, editable by
// an operator until the task starts, frozen after.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { authedFetch } from '../../../auth/authedFetch';
import { render } from '../../../test/render';
import { makeSnapshot } from '../../../test/fixtures/snapshots';
import type { TrackerIssue } from '../../../types/schemas';
import IssueTaskInstructions from '../IssueTaskInstructions';
import { routeFetch } from '../changes/__tests__/fetchMock';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
const mockFetch = vi.mocked(authedFetch);

const issue = (task: TrackerIssue['task']): TrackerIssue =>
  ({
    identifier: 'TASK-1',
    title: 'T',
    state: 'Todo',
    orchestratorState: 'idle',
    task,
  }) as TrackerIssue;

beforeEach(() => {
  mockFetch.mockReset();
  routeFetch(mockFetch, {
    '/api/v1/tasks/TASK-1/instructions': { body: { identifier: 'TASK-1', instructions: 'new' } },
  });
});

describe('IssueTaskInstructions (#251)', () => {
  it('renders nothing for an issue that is not a task', () => {
    render(<IssueTaskInstructions issue={issue(undefined)} />);
    expect(screen.queryByTestId('issue-task-instructions')).not.toBeInTheDocument();
  });

  it('lets an operator edit the instructions before the task starts', async () => {
    render(
      <IssueTaskInstructions
        issue={issue({ instructions: 'Old text', local: true, started: false })}
      />,
    );
    expect(await screen.findByText('Old text')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Edit instructions' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Task instructions' }), {
      target: { value: 'New text' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/tasks/TASK-1/instructions',
        expect.objectContaining({
          method: 'PUT',
          body: JSON.stringify({ instructions: 'New text' }),
        }),
      );
    });
  });

  it('freezes them once the task started, and hides editing from a viewer', async () => {
    const { unmount } = render(
      <IssueTaskInstructions issue={issue({ instructions: 'Ran with this', started: true })} />,
    );
    expect(await screen.findByText(/frozen/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit instructions' })).not.toBeInTheDocument();
    unmount();
    render(<IssueTaskInstructions issue={issue({ instructions: 'x', started: false })} />, {
      snapshot: makeSnapshot({ callerScope: 'viewer' }),
    });
    expect(screen.queryByRole('button', { name: 'Edit instructions' })).not.toBeInTheDocument();
  });
});
