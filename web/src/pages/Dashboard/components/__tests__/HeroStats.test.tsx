import { render, screen, fireEvent } from '@testing-library/react';
import { describe, expect, it, vi, beforeEach } from 'vitest';
import { MemoryRouter } from 'react-router';
import type * as ReactRouter from 'react-router';
import { HeroStats } from '../HeroStats';
import { automationsFiredToday } from '../dashboardMetrics';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { useUIStore } from '../../../../store/uiStore';

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
  it('counts both input-required and pending-input-resume entries as blocked', () => {
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

    expect(screen.getByText('Input Required')).toBeInTheDocument();
    expect(screen.getByText('2')).toBeInTheDocument();
  });

  it('keeps six-column stats for desktop instead of tablet widths', () => {
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
    expect(stats.className).toContain('lg:grid-cols-6');
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
});
