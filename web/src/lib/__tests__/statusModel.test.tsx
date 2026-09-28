// CORE-076 — one status model for AppHeader, LiveOpsStrip, HeroStats and the
// operator queue. Every surface reads its counts from summarizeStatus, so a
// single snapshot must yield the same numbers everywhere.
import { describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';
import AppHeader from '../../layout/AppHeader';
import { LiveOpsStrip } from '../../pages/Dashboard/components/LiveOpsStrip';
import { HeroStats } from '../../pages/Dashboard/components/HeroStats';
import { buildOperatorQueueItems } from '../operatorQueue';
import { STATUS_META, summarizeStatus, toneColor, type StatusKey } from '../statusModel';
import { PENDING_RESUME_CONTEXT_PREFIX } from '../../utils/inputRequired';
import { render } from '../../test/render';
import { makeRetryRow, makeRunningRow, makeSnapshot } from '../../test/fixtures/snapshots';
import { useItervoxStore } from '../../store/itervoxStore';
import { StateSnapshotSchema, type StateSnapshot } from '../../types/schemas';

const QUEUED_AT = '2026-09-01T00:00:00Z';

function mixedSnapshot(): StateSnapshot {
  return makeSnapshot({
    running: [
      makeRunningRow({ identifier: 'ENG-1', sessionId: 's-1' }),
      makeRunningRow({ identifier: 'ENG-2', sessionId: 's-2' }),
    ],
    paused: ['ENG-3'],
    retrying: [makeRetryRow({ identifier: 'ENG-4' })],
    maxConcurrentAgents: 4,
    inputRequired: [
      {
        identifier: 'ENG-5',
        sessionId: 'a',
        state: 'input_required',
        context: 'q?',
        queuedAt: QUEUED_AT,
      },
      {
        identifier: 'ENG-6',
        sessionId: 'b',
        state: 'input_required',
        context: 'q2?',
        queuedAt: QUEUED_AT,
      },
      {
        identifier: 'ENG-7',
        sessionId: 'c',
        state: 'pending_input_resume',
        context: 'reply queued',
        queuedAt: QUEUED_AT,
      },
    ],
  });
}

function countIn(container: HTMLElement, key: StatusKey): number {
  const el = container.querySelector(`[data-status-key="${key}"]`);
  if (!el) throw new Error(`no [data-status-key="${key}"] in ${container.dataset.testid ?? '?'}`);
  return Number(el.getAttribute('data-status-count'));
}

describe('statusModel', () => {
  it('needsInput excludes pending_input_resume', () => {
    const summary = summarizeStatus(mixedSnapshot(), { sseConnected: true });
    expect(summary.needsInput).toBe(2);
    expect(summary.resuming).toBe(1);
  });

  it('rows without state fall back through inputRequiredRowState', () => {
    const snap = makeSnapshot();
    snap.inputRequired = [
      // Older daemons omit `state`; the context prefix decides.
      {
        identifier: 'OLD-1',
        sessionId: 'x',
        state: undefined as never,
        context: `${PENDING_RESUME_CONTEXT_PREFIX} more`,
        queuedAt: QUEUED_AT,
      },
      {
        identifier: 'OLD-2',
        sessionId: 'y',
        state: undefined as never,
        context: 'Which branch?',
        queuedAt: QUEUED_AT,
      },
    ];
    const summary = summarizeStatus(snap, { sseConnected: true });
    expect(summary.resuming).toBe(1);
    expect(summary.needsInput).toBe(1);
    const queue = buildOperatorQueueItems(snap, []);
    expect(
      queue.groups.find((g) => g.group === 'needs_input')?.items.map((i) => i.identifier),
    ).toEqual(['OLD-2']);
    expect(
      queue.groups.find((g) => g.group === 'resuming')?.items.map((i) => i.identifier),
    ).toEqual(['OLD-1']);
  });

  it('offline with running rows reports live=false and running>0', () => {
    const summary = summarizeStatus(mixedSnapshot(), { sseConnected: false });
    expect(summary.live).toBe(false);
    expect(summary.running).toBe(2);
    expect(summary.activity).toBe('active');
  });

  it('connected with zero running reports live=true and running=0', () => {
    const summary = summarizeStatus(makeSnapshot({ running: [] }), { sseConnected: true });
    expect(summary.live).toBe(true);
    expect(summary.running).toBe(0);
    expect(summary.activity).toBe('waiting');
  });

  it('no snapshot reports offline activity and zero counts', () => {
    const summary = summarizeStatus(null, { sseConnected: false });
    expect(summary.activity).toBe('offline');
    expect(summary.running + summary.paused + summary.retrying + summary.needsInput).toBe(0);
    expect(summary.headline).toBe('idle');
  });

  // M5-close — a pending reply must not hide a reply that is still needed:
  // Needs input outranks Resuming in the header chip.
  it('headline prefers Needs input over Resuming when both are present', () => {
    expect(summarizeStatus(mixedSnapshot(), { sseConnected: true }).headline).toBe('running');
    const snap = mixedSnapshot();
    snap.running = [];
    expect(summarizeStatus(snap, { sseConnected: true }).headline).toBe('input_required');
    snap.inputRequired = snap.inputRequired?.filter((r) => r.state === 'pending_input_resume');
    expect(summarizeStatus(snap, { sseConnected: true }).headline).toBe('pending_input_resume');
  });

  // M5-close — web and TUI must agree on rows with no `state` (older
  // daemons). The wire boundary decides: Zod's tolerantEnum defaults a
  // missing state to input_required, so such a row counts as Needs input
  // even when its context carries the pending-resume prefix. The TUI aligns
  // to this default (Go side, same round).
  it('a parsed row without state counts as Needs input (the wire default)', () => {
    const raw = {
      ...makeSnapshot(),
      inputRequired: [
        {
          identifier: 'OLD-1',
          sessionId: 'x',
          context: `${PENDING_RESUME_CONTEXT_PREFIX} more`,
          queuedAt: QUEUED_AT,
        },
      ],
    };
    const parsed = StateSnapshotSchema.parse(raw);
    expect(parsed.inputRequired?.[0]?.state).toBe('input_required');
    const summary = summarizeStatus(parsed, { sseConnected: true });
    expect(summary.needsInput).toBe(1);
    expect(summary.resuming).toBe(0);
  });

  it('mixed snapshot yields identical counts in AppHeader, LiveOpsStrip, HeroStats and operatorQueue', () => {
    const snap = mixedSnapshot();
    render(
      <>
        <div data-testid="surface-header">
          <AppHeader />
        </div>
        <LiveOpsStrip />
        <HeroStats />
      </>,
      { snapshot: snap },
    );
    useItervoxStore.setState({ sseConnected: true });
    const expected = summarizeStatus(snap, { sseConnected: true });
    const queue = buildOperatorQueueItems(snap, []);
    const queueCount = (group: string) =>
      queue.groups.find((g) => g.group === group)?.items.length ?? 0;
    const surfaces = [
      screen.getByTestId('surface-header'),
      screen.getByTestId('live-ops-strip'),
      screen.getByTestId('hero-stats'),
    ];
    const keys: [StatusKey, number, number][] = [
      ['running', expected.running, 2],
      ['paused', expected.paused, queueCount('paused')],
      ['retrying', expected.retrying, queueCount('retrying')],
      ['input_required', expected.needsInput, queueCount('needs_input')],
      ['pending_input_resume', expected.resuming, queueCount('resuming')],
    ];
    for (const [key, modelCount, queueN] of keys) {
      expect(queueN, `operatorQueue ${key}`).toBe(modelCount);
      for (const surface of surfaces) {
        expect(countIn(surface, key), `${surface.dataset.testid ?? ''} ${key}`).toBe(modelCount);
      }
    }
    // One vocabulary: every surface labels needs-input with STATUS_META.
    for (const surface of surfaces) {
      expect(
        within(surface).getAllByText(new RegExp(STATUS_META.input_required.label, 'i')).length,
      ).toBeGreaterThan(0);
    }
  });

  it('LiveOpsStrip says Active (not Live) while agents run; Live is only the connection', () => {
    render(<LiveOpsStrip />, { snapshot: mixedSnapshot() });
    const strip = screen.getByTestId('live-ops-strip');
    expect(within(strip).getByText('Active')).toBeInTheDocument();
    expect(within(strip).queryByText('Live')).not.toBeInTheDocument();
  });

  it('toneColor maps every tone to a theme token (neutral stays default)', () => {
    expect(toneColor('success')).toBe('var(--success)');
    expect(toneColor('warning')).toBe('var(--warning)');
    expect(toneColor('danger')).toBe('var(--danger)');
    expect(toneColor('info')).toBe('var(--accent)');
    expect(toneColor('neutral')).toBeUndefined();
  });
});
