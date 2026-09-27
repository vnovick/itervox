import { render, screen, fireEvent } from '@testing-library/react';
import { describe, expect, it, vi, beforeEach } from 'vitest';
import { MemoryRouter } from 'react-router';
import type * as ReactRouter from 'react-router';
import { HeroStats } from '../HeroStats';
import { automationsFiredToday } from '../dashboardMetrics';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { useUIStore } from '../../../../store/uiStore';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';
import { StateSnapshotSchema } from '../../../../types/schemas';

const navigateMock = vi.fn();

vi.mock('react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof ReactRouter>();
  return {
    ...actual,
    useNavigate: () => navigateMock,
  };
});

beforeEach(() => {
  navigateMock.mockReset();
  useUIStore.setState({ timelineAutomationOnly: false });
});

describe('HeroStats', () => {
  it('counts input-required and pending-input-resume entries separately (CORE-076)', () => {
    useItervoxStore.setState({
      snapshot: {
        generatedAt: new Date().toISOString(),
        counts: { running: 0, retrying: 0, paused: 0 },
        running: [],
        retrying: [],
        paused: [],
        maxConcurrentAgents: 3,
        inputRequired: [
          {
            identifier: 'ENG-1',
            sessionId: 's1',
            state: 'input_required',
            context: 'Need approval',
            queuedAt: new Date().toISOString(),
          },
          {
            identifier: 'ENG-2',
            sessionId: 's2',
            state: 'pending_input_resume',
            context: 'Waiting to resume',
            queuedAt: new Date().toISOString(),
          },
        ],
      },
    });

    render(
      <MemoryRouter>
        <HeroStats />
      </MemoryRouter>,
    );

    // CORE-076 — pending_input_resume is no longer counted as needing input.
    const needs = screen.getByText('Needs input').closest('[data-status-key]');
    expect(needs).toHaveAttribute('data-status-count', '1');
    const resuming = screen.getByText('Resuming').closest('[data-status-key]');
    expect(resuming).toHaveAttribute('data-status-count', '1');
  });

  it('keeps one-row stats for desktop instead of tablet widths', () => {
    useItervoxStore.setState({
      snapshot: {
        generatedAt: new Date().toISOString(),
        counts: { running: 0, retrying: 0, paused: 0 },
        running: [],
        retrying: [],
        paused: [],
        maxConcurrentAgents: 3,
        inputRequired: [],
      },
    });

    render(
      <MemoryRouter>
        <HeroStats />
      </MemoryRouter>,
    );

    const stats = screen.getByTestId('hero-stats');
    expect(stats.className).toContain('lg:grid-cols-7');
    expect(stats.className).not.toContain('md:grid-cols-6');
  });
});

describe('automationsFiredToday (T-8)', () => {
  const isoToday = new Date().toISOString();
  const isoYesterday = new Date(Date.now() - 24 * 60 * 60 * 1000 - 1000).toISOString();

  it('counts only history rows with an automationId AND finishedAt >= today', () => {
    const rows = [
      { automationId: 'cron-a', finishedAt: isoToday },
      { automationId: 'cron-b', finishedAt: isoToday },
      { automationId: 'cron-c', finishedAt: isoYesterday },
      { automationId: '', finishedAt: isoToday },
      { finishedAt: isoToday },
    ];
    expect(automationsFiredToday(rows)).toBe(2);
  });

  it('returns 0 when no rows match', () => {
    expect(automationsFiredToday([])).toBe(0);
    expect(automationsFiredToday([{ automationId: 'a', finishedAt: isoYesterday }])).toBe(0);
  });
});

describe('HeroStats — Automations triggered today tile', () => {
  it('renders 0 (not "—") when no automation has fired today', () => {
    useItervoxStore.setState({
      snapshot: {
        generatedAt: new Date().toISOString(),
        counts: { running: 0, retrying: 0, paused: 0 },
        running: [],
        retrying: [],
        paused: [],
        maxConcurrentAgents: 3,
        inputRequired: [],
        history: [],
      },
    });
    render(
      <MemoryRouter>
        <HeroStats />
      </MemoryRouter>,
    );
    const tile = screen.getByTestId('hero-stat-automations-today');
    expect(tile.textContent).toContain('0');
    expect(tile.textContent).toContain('fired today');
  });

  it("renders today's automation-driven history rows count", () => {
    const today = new Date().toISOString();
    const yesterday = new Date(Date.now() - 24 * 60 * 60 * 1000 - 1000).toISOString();
    useItervoxStore.setState({
      snapshot: {
        generatedAt: new Date().toISOString(),
        counts: { running: 0, retrying: 0, paused: 0 },
        running: [],
        retrying: [],
        paused: [],
        maxConcurrentAgents: 3,
        inputRequired: [],
        history: [
          {
            identifier: 'ENG-1',
            startedAt: today,
            finishedAt: today,
            status: 'succeeded',
            elapsedMs: 1000,
            turnCount: 1,
            tokens: 10,
            inputTokens: 5,
            outputTokens: 5,
            automationId: 'cron-1',
          },
          {
            identifier: 'ENG-2',
            startedAt: today,
            finishedAt: today,
            status: 'succeeded',
            elapsedMs: 1000,
            turnCount: 1,
            tokens: 10,
            inputTokens: 5,
            outputTokens: 5,
            automationId: 'cron-2',
          },
          {
            identifier: 'ENG-3',
            startedAt: today,
            finishedAt: today,
            status: 'succeeded',
            elapsedMs: 1000,
            turnCount: 1,
            tokens: 10,
            inputTokens: 5,
            outputTokens: 5,
            automationId: 'cron-3',
          },
          {
            identifier: 'ENG-OLD',
            startedAt: yesterday,
            finishedAt: yesterday,
            status: 'succeeded',
            elapsedMs: 1000,
            turnCount: 1,
            tokens: 10,
            inputTokens: 5,
            outputTokens: 5,
            automationId: 'cron-old',
          },
          {
            identifier: 'ENG-MANUAL',
            startedAt: today,
            finishedAt: today,
            status: 'succeeded',
            elapsedMs: 1000,
            turnCount: 1,
            tokens: 10,
            inputTokens: 5,
            outputTokens: 5,
          },
        ],
      },
    });
    render(
      <MemoryRouter>
        <HeroStats />
      </MemoryRouter>,
    );
    const tile = screen.getByTestId('hero-stat-automations-today');
    expect(tile.textContent).toContain('3');
  });

  it('clicking the tile filters Timeline to automation-only and navigates', () => {
    useItervoxStore.setState({
      snapshot: {
        generatedAt: new Date().toISOString(),
        counts: { running: 0, retrying: 0, paused: 0 },
        running: [],
        retrying: [],
        paused: [],
        maxConcurrentAgents: 3,
        inputRequired: [],
        history: [],
      },
    });
    render(
      <MemoryRouter>
        <HeroStats />
      </MemoryRouter>,
    );
    const tile = screen.getByTestId('hero-stat-automations-today');
    fireEvent.click(tile);
    expect(useUIStore.getState().timelineAutomationOnly).toBe(true);
    expect(navigateMock).toHaveBeenCalledWith('/timeline');
  });

  // CORE-091 — fleet token/cost tile from snapshot.totals (Go side pending:
  // see .plans/execution/M6-W3/rounds.md "Go handoff").
  describe('estimated cost tile (CORE-091)', () => {
    const renderWith = (totals: unknown) => {
      useItervoxStore.setState({
        snapshot: StateSnapshotSchema.parse({ ...makeSnapshot(), totals }),
      });
      render(
        <MemoryRouter>
          <HeroStats />
        </MemoryRouter>,
      );
      return screen.queryByTestId('hero-stat-cost');
    };

    it("renders the estimated-cost tile with 'Claude runs only' when codexRuns > 0", () => {
      const tile = renderWith({
        inputTokens: 900_000,
        outputTokens: 334_000,
        costUsdEstimated: 4.567,
        costCoverage: { claudeRuns: 3, codexRuns: 2 },
      });
      expect(tile).toHaveTextContent('$4.57');
      expect(tile).toHaveTextContent(/estimated, Claude runs only/i);
      expect(tile).toHaveTextContent('1.2M tokens');
      // Eight tiles: two rows of four on large screens, one row from 2xl.
      expect(screen.getByTestId('hero-stats').className).toContain('lg:grid-cols-4');
    });

    it("renders '—' for cost when costUsdEstimated is null", () => {
      const tile = renderWith({
        inputTokens: 10,
        outputTokens: 5,
        costUsdEstimated: null,
        costCoverage: { claudeRuns: 0, codexRuns: 1 },
      });
      expect(tile).toHaveTextContent('—');
      expect(tile).not.toHaveTextContent('$0');
    });

    it('says estimated (no Codex caveat) when only Claude runs contributed', () => {
      const tile = renderWith({
        inputTokens: 100,
        outputTokens: 50,
        costUsdEstimated: 0.01,
        costCoverage: { claudeRuns: 2, codexRuns: 0 },
      });
      expect(tile).toHaveTextContent('$0.01');
      expect(tile).toHaveTextContent(/^.*estimated.*$/i);
      expect(tile).not.toHaveTextContent(/Claude runs only/i);
    });

    it('hides the tile when the daemon sends no totals (older daemon)', () => {
      expect(renderWith(undefined)).toBeNull();
    });
  });
});
