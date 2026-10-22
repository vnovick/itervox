// #218 — steering chips: the configured phrases as one-click buttons, and
// the issue steer box that sends them (or a typed message) to the running
// run through the Sessions steering endpoint.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { authedFetch } from '../../../auth/authedFetch';
import { render } from '../../../test/render';
import { makeSnapshot } from '../../../test/fixtures/snapshots';
import { SteeringChips } from '../SteeringChips';
import { IssueSteerBox } from '../IssueSteerBox';
import { routeFetch } from '../changes/__tests__/fetchMock';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
const mockFetch = vi.mocked(authedFetch);

const phrases = {
  phrases: [
    { label: 'Prove it works', message: 'Prove it works: show me the test output.' },
    { label: 'Reproduce first', message: 'Reproduce the problem first.' },
  ],
};

const liveContext = {
  identifier: 'ENG-1',
  run: { runId: 'run-7', live: true, toolCalls: 0, turnCount: 1, steers: [] },
  changedFiles: [],
  handoffs: [],
};

beforeEach(() => {
  mockFetch.mockReset();
  routeFetch(mockFetch, {
    '/api/v1/steering/phrases': { body: phrases },
    '/api/v1/issues/ENG-1/context': { body: liveContext },
    '/api/v1/sessions/run-7/message': { status: 202, body: { message: { id: 'msg-1' } } },
  });
});

function sentBodies(): unknown[] {
  return mockFetch.mock.calls
    .filter(([url]) => url === '/api/v1/sessions/run-7/message')
    .map(([, init]) => {
      const body = init?.body;
      return typeof body === 'string' ? (JSON.parse(body) as unknown) : undefined;
    });
}

describe('SteeringChips (#218)', () => {
  it('renders one chip per configured phrase and sends its message', async () => {
    const onSend = vi.fn();
    render(<SteeringChips onSend={onSend} />);
    const group = await screen.findByRole('group', { name: 'Steering phrases' });
    const chip = within(group).getByRole('button', { name: 'Reproduce first' });
    expect(chip).toHaveAttribute('title', 'Reproduce the problem first.');
    fireEvent.click(chip);
    expect(onSend).toHaveBeenCalledWith('Reproduce the problem first.');
  });

  it('renders nothing when no phrases are configured', async () => {
    routeFetch(mockFetch, { '/api/v1/steering/phrases': { body: { phrases: [] } } });
    const { container } = render(<SteeringChips onSend={vi.fn()} />);
    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalled();
    });
    expect(container.querySelector('[role="group"]')).toBeNull();
  });
});

describe('IssueSteerBox (#218)', () => {
  it('sends a chip and a typed message to the live run', async () => {
    render(<IssueSteerBox identifier="ENG-1" />);
    const group = await screen.findByRole('group', { name: 'Steering phrases' });
    fireEvent.click(within(group).getByRole('button', { name: 'Prove it works' }));
    await waitFor(() => {
      expect(sentBodies()).toEqual([{ message: 'Prove it works: show me the test output.' }]);
    });
    fireEvent.change(screen.getByRole('textbox', { name: 'Message to the agent on ENG-1' }), {
      target: { value: 'Use the existing helper' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Send' }));
    await waitFor(() => {
      expect(sentBodies()).toHaveLength(2);
    });
    expect(sentBodies()[1]).toEqual({ message: 'Use the existing helper' });
  });

  it('is hidden without a live run and from a viewer', async () => {
    routeFetch(mockFetch, {
      '/api/v1/steering/phrases': { body: phrases },
      '/api/v1/issues/ENG-1/context': {
        body: { ...liveContext, run: { ...liveContext.run, live: false } },
      },
    });
    const { unmount } = render(<IssueSteerBox identifier="ENG-1" />);
    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith('/api/v1/issues/ENG-1/context', expect.anything());
    });
    expect(screen.queryByTestId('issue-steer-box')).not.toBeInTheDocument();
    unmount();

    routeFetch(mockFetch, {
      '/api/v1/steering/phrases': { body: phrases },
      '/api/v1/issues/ENG-1/context': { body: liveContext },
    });
    render(<IssueSteerBox identifier="ENG-1" />, {
      snapshot: makeSnapshot({ callerScope: 'viewer' }),
    });
    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith('/api/v1/issues/ENG-1/context', expect.anything());
    });
    expect(screen.queryByTestId('issue-steer-box')).not.toBeInTheDocument();
  });
});
