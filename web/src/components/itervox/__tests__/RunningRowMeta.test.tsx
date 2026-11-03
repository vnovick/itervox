import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { RunningRowMeta } from '../RunningRowMeta';
import type { RunningRow } from '../../../types/schemas';

const row: RunningRow = {
  identifier: 'ENG-1',
  state: 'Todo',
  turnCount: 1,
  tokens: 10,
  inputTokens: 5,
  outputTokens: 5,
  elapsedMs: 1000,
  startedAt: '2026-10-01T10:00:00Z',
};

describe('RunningRowMeta — out of scope (#182)', () => {
  it('marks a run whose issue left the tracker scope', () => {
    render(<RunningRowMeta row={{ ...row, outOfScope: true }} profile={undefined} now={0} />);
    expect(screen.getByTestId('running-row-out-of-scope-ENG-1')).toHaveTextContent('out of scope');
  });

  it('shows no marker for an in-scope run', () => {
    render(<RunningRowMeta row={row} profile={undefined} now={0} />);
    expect(screen.queryByTestId('running-row-out-of-scope-ENG-1')).toBeNull();
  });
});
