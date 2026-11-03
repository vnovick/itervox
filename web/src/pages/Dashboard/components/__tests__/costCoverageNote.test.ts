// The cost tile's note names the runs the estimate leaves out: backends with
// runs that never reported a cost. Older daemons send only Claude/Codex counts.
import { describe, expect, it } from 'vitest';
import { costCoverageNote } from '../dashboardMetrics';
import type { Totals } from '../../../../types/schemas';

const base = { inputTokens: 900, outputTokens: 90, costUsdEstimated: 0.31 };

describe('costCoverageNote', () => {
  it('names each backend with runs that reported no cost', () => {
    const totals: Totals = {
      ...base,
      costCoverage: {
        claudeRuns: 1,
        codexRuns: 1,
        runsByBackend: { claude: 1, opencode: 2, cursor: 1, codex: 1 },
        costBackends: ['claude', 'opencode'],
      },
    };
    expect(costCoverageNote(totals)).toBe('estimated, excludes Codex, Cursor runs');
  });

  it('says only "estimated" when every backend with runs reported a cost', () => {
    const totals: Totals = {
      ...base,
      costCoverage: {
        claudeRuns: 0,
        codexRuns: 0,
        runsByBackend: { opencode: 3 },
        costBackends: ['opencode'],
      },
    };
    expect(costCoverageNote(totals)).toBe('estimated');
  });

  it('falls back to the Claude / Codex counts from an older daemon', () => {
    expect(costCoverageNote({ ...base, costCoverage: { claudeRuns: 2, codexRuns: 1 } })).toBe(
      'estimated, Claude runs only',
    );
    expect(costCoverageNote({ ...base, costCoverage: { claudeRuns: 2, codexRuns: 0 } })).toBe(
      'estimated',
    );
    expect(costCoverageNote(base)).toBe('estimated');
  });
});
