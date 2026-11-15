import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { RunRow } from '../RunRow';
import type { NormalisedSession } from '../types';

const run: NormalisedSession = {
  identifier: 'ENG-1',
  startedAt: '2026-10-01T10:10:00Z',
  finishedAt: '2026-10-01T10:30:00Z',
  elapsedMs: 20 * 60_000,
  turnCount: 2,
  tokens: 18_000,
  status: 'succeeded',
};

const viewStart = new Date('2026-10-01T10:00:00Z').getTime();
const viewEnd = new Date('2026-10-01T10:40:00Z').getTime();

describe('RunRow on the shared time axis', () => {
  it('draws the expanded Main bar on the same columns and span as the run bar', () => {
    render(
      <RunRow
        session={run}
        subagents={[]}
        viewStart={viewStart}
        viewEnd={viewEnd}
        expanded
        selectedSubagentIdx={null}
        runNumber={1}
        onToggleExpand={() => {}}
        onSelectSubagent={() => {}}
      />,
    );
    const runBar = screen.getByTestId('timeline-run-bar');
    const mainBar = screen.getByTestId('timeline-main-bar');
    expect(runBar.style.left).toBe('25%');
    expect(runBar.style.width).toBe('50%');
    expect(mainBar.style.left).toBe(runBar.style.left);
    expect(mainBar.style.width).toBe(runBar.style.width);
    // Both rows use the one grid, so their tracks start and end together.
    const runRow = screen.getByRole('button', { name: /Run #1/ });
    const mainRow = screen.getByRole('button', { name: 'Main agent log' });
    const cols = (el: HTMLElement) =>
      [...el.classList].filter((c) => c.includes('grid-cols')).join(' ');
    expect(cols(mainRow)).toBe(cols(runRow));
  });

  it('fills a finished run with the muted status fill and a done tag', () => {
    render(
      <RunRow
        session={run}
        subagents={[]}
        viewStart={viewStart}
        viewEnd={viewEnd}
        expanded={false}
        selectedSubagentIdx={null}
        runNumber={1}
        onToggleExpand={() => {}}
        onSelectSubagent={() => {}}
      />,
    );
    expect(screen.getByTestId('timeline-run-bar')).toHaveClass('bg-theme-dot-idle/85');
    expect(screen.getByText('done')).toHaveClass('tag', 'pill-ok');
  });
});
