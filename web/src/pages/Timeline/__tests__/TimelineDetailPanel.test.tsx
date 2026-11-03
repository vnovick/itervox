import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';

import { TimelineDetailPanel } from '../components/TimelineDetailPanel';
import type { NormalisedSession } from '../../../components/itervox/timeline/types';

function panel(selectedSession: NormalisedSession) {
  return (
    <TimelineDetailPanel
      selectedId="ENG-1"
      selectedGroup={null}
      anyLive={selectedSession.status === 'live'}
      logsForSelected={[]}
      viewStart={0}
      viewEnd={1}
      expandedRunAt={null}
      selectedSubagentIdx={null}
      onToggleExpand={() => {}}
      onSelectSubagent={() => {}}
      expandedRun={null}
      selectedSession={selectedSession}
      isSelectedLive={selectedSession.status === 'live'}
      sublogs={[]}
      subagentsForExpanded={[]}
      validSubagentIdx={null}
      logPanelSlice={[]}
    />
  );
}

const base: NormalisedSession = {
  identifier: 'ENG-1',
  startedAt: '2026-10-01T00:00:00Z',
  elapsedMs: 1000,
  turnCount: 1,
  tokens: 0,
  status: 'succeeded',
  promptDigest: 'c'.repeat(64),
  steers: [{ text: 'focus on tests', at: '2026-10-01T00:00:10Z', delivery: 'resume' }],
};

describe('TimelineDetailPanel run provenance (#186)', () => {
  it('shows the selected finished run’s prompt version and steering timeline', () => {
    render(panel(base));
    expect(screen.getByTestId('run-prompt-digest')).toHaveTextContent('cccccccccccc');
    expect(screen.getByTestId('run-steers')).toHaveTextContent('focus on tests');
  });

  it('shows no provenance for a live run (it is recorded when the run finishes)', () => {
    render(panel({ ...base, status: 'live' }));
    expect(screen.queryByTestId('run-provenance')).not.toBeInTheDocument();
  });
});
