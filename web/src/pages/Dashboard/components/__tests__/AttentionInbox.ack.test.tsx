// CORE-175 — acknowledge a failed / stalled attention item. The daemon side
// (event-loop-mediated POST /api/v1/issues/{id}/failures/ack, snapshot
// `failureAcks`, capability `failure_ack`) is a Go handoff (M6-W3 rounds.md);
// the UI is built against that contract and hidden without the capability.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { authedFetch } from '../../../../auth/authedFetch';
import { AttentionInbox } from '../AttentionInbox';
import { render } from '../../../../test/render';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';
import { makeIssue } from '../../../../test/fixtures/issues';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { useToastStore } from '../../../../store/toastStore';
import { buildOperatorQueueItems } from '../../../../lib/operatorQueue';
import { StateSnapshotSchema, type StateSnapshot } from '../../../../types/schemas';

vi.mock('../../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
const mockFetch = vi.mocked(authedFetch);

const OLD = '2026-09-27T10:00:00Z';
const NEW = '2026-09-27T11:00:00Z';

function failure(identifier: string, occurredAt: string) {
  return {
    kind: 'worker_failed',
    identifier,
    message: 'exit 1',
    occurredAt,
    recordedAt: occurredAt,
    count: 1,
  };
}

function snap(extra: Record<string, unknown> = {}): StateSnapshot {
  return StateSnapshotSchema.parse({
    ...makeSnapshot(),
    recentFailures: [failure('ENG-10', OLD)],
    ...extra,
  });
}

function urlOf(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input;
  return input instanceof URL ? input.href : input.url;
}

function route(ackStatus = 202) {
  mockFetch.mockImplementation((input: RequestInfo | URL) => {
    const url = urlOf(input);
    if (url === '/api/v1/issues')
      return Promise.resolve(Response.json([makeIssue({ identifier: 'ENG-10' })]));
    if (url.endsWith('/failures/ack')) {
      return Promise.resolve(
        ackStatus === 202
          ? Response.json({ queued: true }, { status: 202 })
          : Response.json(
              { error: { code: 'boom', message: 'ack failed' } },
              { status: ackStatus },
            ),
      );
    }
    return Promise.resolve(Response.json({}));
  });
}

beforeEach(() => {
  mockFetch.mockReset();
  useToastStore.setState({ toasts: [], _timers: new Map() });
});

describe('AttentionInbox acknowledge (CORE-175)', () => {
  it('no Acknowledge action without the failure_ack capability', () => {
    route();
    render(<AttentionInbox onSelectIssue={vi.fn()} />, { snapshot: snap() });
    const item = screen.getByTestId('attention-item-failed-ENG-10');
    expect(within(item).queryByRole('button', { name: /acknowledge/i })).not.toBeInTheDocument();
  });

  it('Acknowledge posts upTo for the newest failure and hides the item optimistically', async () => {
    route();
    render(<AttentionInbox onSelectIssue={vi.fn()} />, {
      snapshot: snap({ capabilities: ['failure_ack'] }),
    });
    const item = screen.getByTestId('attention-item-failed-ENG-10');
    fireEvent.click(within(item).getByRole('button', { name: 'Acknowledge ENG-10' }));
    await waitFor(() => {
      expect(screen.queryByTestId('attention-item-failed-ENG-10')).not.toBeInTheDocument();
    });
    const call = mockFetch.mock.calls.find(([u]) => urlOf(u).endsWith('/failures/ack'));
    expect(urlOf(call?.[0] as RequestInfo)).toBe('/api/v1/issues/ENG-10/failures/ack');
    expect(call?.[1]?.method).toBe('POST');
    expect(JSON.parse(call?.[1]?.body as string)).toEqual({ upTo: OLD });
  });

  it('a failed acknowledge restores the item and shows an error toast', async () => {
    route(500);
    render(<AttentionInbox onSelectIssue={vi.fn()} />, {
      snapshot: snap({ capabilities: ['failure_ack'] }),
    });
    fireEvent.click(screen.getByRole('button', { name: 'Acknowledge ENG-10' }));
    await waitFor(() => {
      expect(useToastStore.getState().toasts.some((t) => t.variant === 'error')).toBe(true);
    });
    expect(screen.getByTestId('attention-item-failed-ENG-10')).toBeInTheDocument();
    expect(useItervoxStore.getState().snapshot?.failureAcks ?? []).toEqual([]);
  });
});

describe('acknowledge rollback scope (M6-close)', () => {
  it('a failed acknowledge rolls back only failureAcks, keeping snapshots that arrived meanwhile', async () => {
    let reject: ((r: Response) => void) | null = null;
    mockFetch.mockImplementation((input: RequestInfo | URL) => {
      const url = urlOf(input);
      if (url === '/api/v1/issues') return Promise.resolve(Response.json([]));
      if (url.endsWith('/failures/ack')) {
        return new Promise<Response>((resolve) => {
          reject = resolve;
        });
      }
      return Promise.resolve(Response.json({}));
    });
    const before = snap({
      capabilities: ['failure_ack'],
      failureAcks: [{ identifier: 'ENG-99', upTo: OLD }],
    });
    render(<AttentionInbox onSelectIssue={vi.fn()} />, { snapshot: before });
    fireEvent.click(screen.getByRole('button', { name: 'Acknowledge ENG-10' }));
    await waitFor(() => {
      expect(useItervoxStore.getState().snapshot?.failureAcks).toHaveLength(2);
    });
    // A newer snapshot arrives while the request is in flight.
    const newer = {
      ...(useItervoxStore.getState().snapshot as StateSnapshot),
      maxConcurrentAgents: 9,
    };
    useItervoxStore.setState({ snapshot: newer });
    reject?.(Response.json({ error: { code: 'boom', message: 'ack failed' } }, { status: 500 }));
    await waitFor(() => {
      expect(useToastStore.getState().toasts.some((t) => t.variant === 'error')).toBe(true);
    });
    const after = useItervoxStore.getState().snapshot;
    expect(after?.maxConcurrentAgents).toBe(9);
    expect(after?.failureAcks).toEqual([{ identifier: 'ENG-99', upTo: OLD }]);
  });
});

describe('operator queue failure acks (CORE-175)', () => {
  it('hides a failure acknowledged up to its occurredAt; a newer failure surfaces again', () => {
    const acked = snap({ failureAcks: [{ identifier: 'ENG-10', upTo: OLD }] });
    expect(
      buildOperatorQueueItems(acked, []).groups.find((g) => g.group === 'failed'),
    ).toBeUndefined();
    const newer = snap({
      recentFailures: [failure('ENG-10', OLD), failure('ENG-10', NEW)],
      failureAcks: [{ identifier: 'ENG-10', upTo: OLD }],
    });
    const group = buildOperatorQueueItems(newer, []).groups.find((g) => g.group === 'failed');
    expect(group?.items.map((i) => i.identifier)).toEqual(['ENG-10']);
  });
});
