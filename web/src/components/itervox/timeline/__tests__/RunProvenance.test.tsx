import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';

import { RunProvenance } from '../RunProvenance';
import type { NormalisedSession } from '../types';

const DIGEST = '0123456789abcdef'.repeat(4);

function run(over: Partial<NormalisedSession> = {}): NormalisedSession {
  return {
    identifier: 'ENG-1',
    startedAt: '2026-10-01T00:00:00Z',
    finishedAt: '2026-10-01T00:05:00Z',
    elapsedMs: 300_000,
    turnCount: 3,
    tokens: 0,
    status: 'succeeded',
    ...over,
  };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('RunProvenance (#186)', () => {
  it('shows the short prompt digest and copies the full one', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    render(
      <RunProvenance
        run={run({
          runId: 'run-42',
          promptDigest: DIGEST,
          promptParts: { soul: 'aaaaaaaaaaaaaaaa', rules: 'bbbbbbbbbbbbbbbb' },
        })}
      />,
    );
    const code = screen.getByTestId('run-prompt-digest');
    expect(code).toHaveTextContent(/^0123456789ab$/);
    expect(code.getAttribute('title')).toContain('SOUL.md: aaaaaaaaaaaaaaaa');
    expect(code.getAttribute('title')).toContain('INSTRUCTIONS.md: —');
    expect(screen.getByText('run-42')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Copy the full prompt digest' }));
    expect(writeText).toHaveBeenCalledWith(DIGEST);
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Copy the full prompt digest' })).toHaveTextContent(
        'Copied',
      );
    });
  });

  it('renders the steering timeline in delivery order with its path', () => {
    render(
      <RunProvenance
        run={run({
          promptDigest: DIGEST,
          steers: [
            { text: 'use the staging db', at: '2026-10-01T00:01:00Z', delivery: 'live' },
            { text: 'then stop', at: '2026-10-01T00:02:00Z', delivery: 'resume' },
          ],
          steersDropped: 2,
        })}
      />,
    );
    const items = within(screen.getByTestId('run-steers')).getAllByRole('listitem');
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent('live');
    expect(items[0]).toHaveTextContent('use the staging db');
    expect(items[1]).toHaveTextContent('resume');
    expect(items[1]).toHaveTextContent('then stop');
    expect(items[0].querySelector('time')?.getAttribute('dateTime')).toBe('2026-10-01T00:01:00Z');
    expect(screen.getByText('2 earlier messages not kept')).toBeInTheDocument();
  });

  it('says so when a run recorded no prompt version and no messages', () => {
    render(<RunProvenance run={run()} />);
    expect(screen.queryByTestId('run-prompt-digest')).not.toBeInTheDocument();
    expect(screen.getByText('not recorded')).toBeInTheDocument();
    expect(screen.getByText('None delivered to this run')).toBeInTheDocument();
    expect(screen.queryByTestId('run-steers')).not.toBeInTheDocument();
  });
});
