import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import Sessions from '../index';
import type { SessionRow, TranscriptEntry } from '../../../types/schemas';
import type * as SessionsQueries from '../../../queries/sessions';

// #178 Sessions panel: tabs (waiting-for-input first, badged, keyboard
// navigation), transcript from the issue log stream, and a composer that
// replies through provide-input or messages a running session.

const mocks = vi.hoisted(() => ({
  sessions: [] as SessionRow[],
  sendMutate: vi.fn(),
  provideMutate: vi.fn(),
  dismissMutate: vi.fn(),
  fetchNextPage: vi.fn(),
  transcript: [] as TranscriptEntry[],
  hasNextPage: false,
  inlineInput: false,
}));

vi.mock('../../../queries/sessions', async (importOriginal) => ({
  ...(await importOriginal<typeof SessionsQueries>()),
  useSessions: () => ({ sessions: mocks.sessions, isLoading: false }),
  useSendSessionMessage: () => ({ mutate: mocks.sendMutate, isPending: false }),
  useSessionTranscript: (id: string) => ({
    entries: mocks.transcript.map((e) => ({ ...e, text: `${e.text} (${id})` })),
    isLoading: false,
    isError: false,
    hasNextPage: mocks.hasNextPage,
    fetchNextPage: mocks.fetchNextPage,
    isFetchingNextPage: false,
  }),
}));

vi.mock('../../../queries/issues', () => ({
  useProvideInput: () => ({ mutate: mocks.provideMutate, isPending: false }),
  useDismissInput: () => ({ mutate: mocks.dismissMutate, isPending: false }),
}));

vi.mock('../../../queries/logs', () => ({
  useIssueLogs: (identifier: string) => ({
    data: [
      { event: 'text', message: `transcript of ${identifier}`, level: 'INFO', tool: '', time: '' },
    ],
    isLoading: false,
    isError: false,
  }),
}));

vi.mock('../../../store/itervoxStore', () => ({
  useItervoxStore: (sel: (s: unknown) => unknown) =>
    sel({ snapshot: { inlineInput: mocks.inlineInput } }),
}));

vi.mock('../../../components/common/PageMeta', () => ({ default: () => null }));

vi.mock('../../../components/ui/Terminal/Terminal', () => ({
  Terminal: ({ entries }: { entries: Array<{ message: string }> }) => (
    <div data-testid="terminal">
      {entries.map((e) => (
        <div key={e.message}>{e.message}</div>
      ))}
    </div>
  ),
}));

const WAITING: SessionRow = {
  id: 'sess-wait',
  owner: 'itervox',
  identifier: 'ENG-2',
  status: 'input_required',
  inputContext: 'Which database should I use?',
  backend: 'claude',
};
const RUNNING: SessionRow = {
  id: 'sess-run',
  runId: 'run-1',
  owner: 'itervox',
  identifier: 'ENG-1',
  status: 'running',
  profile: 'coder',
  backend: 'claude',
  steering: 'live',
  messages: [
    { id: 'm1', text: 'first', status: 'delivered', delivery: 'live', queuedAt: 'x' },
    { id: 'm2', text: 'second', status: 'delivered', delivery: 'resume', queuedAt: 'x' },
    { id: 'm3', text: 'third', status: 'queued', queuedAt: 'x' },
    { id: 'm4', text: 'fourth', status: 'dropped', reason: 'the run ended', queuedAt: 'x' },
  ],
};
const STARTING: SessionRow = {
  id: '',
  runId: 'run-2',
  owner: 'itervox',
  identifier: 'ENG-3',
  status: 'running',
  backend: 'codex',
  steering: 'resume',
};

function Location() {
  return <div data-testid="location">{useLocation().search}</div>;
}

function renderAt(search = '') {
  return render(
    <MemoryRouter initialEntries={[`/sessions${search}`]}>
      <Routes>
        <Route
          path="/sessions"
          element={
            <>
              <Sessions />
              <Location />
            </>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

const HAND_EXITED: SessionRow = {
  id: 'hand-exited',
  owner: 'external',
  identifier: '',
  title: 'Add health check',
  backend: 'claude',
  status: 'exited',
  cwd: '/work/app',
  updatedAt: '2026-10-15T08:00:00Z',
};
const HAND_BUSY: SessionRow = {
  id: 'hand-busy',
  owner: 'external',
  identifier: '',
  backend: 'codex',
  status: 'running',
  cwd: '/work/app/svc',
  inUseReason: 'codex process 42 has the transcript open',
};
beforeEach(() => {
  mocks.sessions = [WAITING, RUNNING, STARTING];
  mocks.inlineInput = false;
  mocks.fetchNextPage.mockReset();
  mocks.transcript = [
    { role: 'user', kind: 'text', text: 'Add a health check' },
    { role: 'assistant', kind: 'tool', text: 'Edit' },
    { role: 'assistant', kind: 'text', text: 'Done', truncated: true },
  ];
  mocks.hasNextPage = false;
  mocks.sendMutate.mockReset();
  mocks.provideMutate.mockReset();
  mocks.dismissMutate.mockReset();
});

describe('Sessions page', () => {
  it('lists sessions waiting for input first, badged, and selects the first', () => {
    renderAt();
    const tabs = screen.getAllByRole('tab');
    expect(tabs.map((t) => within(t).getByText(/ENG-\d/).textContent)).toEqual([
      'ENG-2',
      'ENG-1',
      'ENG-3',
    ]);
    expect(within(tabs[0]).getByTestId('session-badge-input')).toHaveTextContent('needs input');
    expect(within(tabs[1]).queryByTestId('session-badge-input')).toBeNull();
    expect(screen.getByTestId('sessions-waiting')).toHaveTextContent('1 waiting');
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('transcript of ENG-2')).toBeInTheDocument();
  });

  it('moves between tabs with the keyboard and keeps the selection in ?id=', async () => {
    const user = userEvent.setup();
    renderAt();
    screen.getAllByRole('tab')[0].focus();
    await user.keyboard('{ArrowDown}');
    expect(screen.getAllByRole('tab')[1]).toHaveAttribute('aria-selected', 'true');
    expect(screen.getAllByRole('tab')[1]).toHaveFocus();
    expect(screen.getByTestId('location')).toHaveTextContent('?id=sess-run');
    await user.keyboard('{End}');
    expect(screen.getByTestId('location')).toHaveTextContent('?id=run-2');
    await user.keyboard('{ArrowDown}');
    expect(screen.getByTestId('location')).toHaveTextContent('?id=sess-wait');
    await user.keyboard('{ArrowUp}');
    expect(screen.getByTestId('location')).toHaveTextContent('?id=run-2');
    await user.keyboard('{Home}');
    expect(screen.getByTestId('location')).toHaveTextContent('?id=sess-wait');
  });

  it('opens the tab named by ?id=', () => {
    renderAt('?id=sess-run');
    expect(screen.getAllByRole('tab')[1]).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByTestId('session-id')).toHaveTextContent('session sess-run');
  });

  it('replies to a waiting session through provide-input', async () => {
    const user = userEvent.setup();
    renderAt('?id=sess-wait');
    expect(screen.getByText('Which database should I use?')).toBeInTheDocument();
    await user.type(screen.getByRole('textbox', { name: /reply to the agent/i }), 'Use Postgres');
    await user.click(screen.getByTestId('input-reply-send'));
    expect(mocks.provideMutate).toHaveBeenCalledWith(
      { identifier: 'ENG-2', message: 'Use Postgres' },
      expect.anything(),
    );
    expect(mocks.sendMutate).not.toHaveBeenCalled();
  });

  it('sends a message to a running session and shows where each message is', async () => {
    const user = userEvent.setup();
    renderAt('?id=sess-run');
    expect(screen.getByTestId('session-steering')).toHaveTextContent('into the running turn');
    const statuses = screen.getAllByTestId('session-message').map((li) => li.textContent);
    expect(statuses).toEqual([
      'firstdelivered live',
      'seconddelivered on resume',
      'thirdqueued',
      'fourthnot delivered: the run ended',
    ]);
    await user.type(screen.getByRole('textbox', { name: /message to the agent/i }), 'Add tests');
    await user.click(screen.getByRole('button', { name: 'Send' }));
    expect(mocks.sendMutate).toHaveBeenCalledWith(
      { id: 'sess-run', message: 'Add tests' },
      expect.anything(),
    );
    expect(mocks.provideMutate).not.toHaveBeenCalled();
  });

  it('addresses a session by its run id until the CLI reports one, and says Codex waits for resume', async () => {
    const user = userEvent.setup();
    renderAt('?id=run-2');
    expect(screen.getByTestId('session-id')).toHaveTextContent('session starting');
    expect(screen.getByTestId('session-steering')).toHaveTextContent('when the current turn ends');
    await user.type(screen.getByRole('textbox', { name: /message to the agent/i }), 'hi');
    await user.click(screen.getByRole('button', { name: 'Send' }));
    expect(mocks.sendMutate).toHaveBeenCalledWith(
      { id: 'run-2', message: 'hi' },
      expect.anything(),
    );
  });

  it('keeps a session itervox does not own read-only', () => {
    mocks.sessions = [{ ...RUNNING, owner: 'external' }];
    renderAt();
    expect(screen.getByTestId('session-readonly')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Send' })).toBeNull();
    expect(screen.queryByRole('textbox')).toBeNull();
  });

  it('shows an empty state with no sessions', () => {
    mocks.sessions = [];
    renderAt();
    expect(screen.getByTestId('sessions-empty')).toHaveTextContent('No agent sessions are running');
  });

  describe('sessions started outside itervox (#178 step 3)', () => {
    it('lists them after itervox sessions, greyed and labeled, named by title or directory', () => {
      mocks.sessions = [WAITING, RUNNING, HAND_EXITED, HAND_BUSY];
      renderAt();
      const tabs = screen.getAllByRole('tab');
      expect(tabs.map((t) => t.getAttribute('data-owner'))).toEqual([
        'itervox',
        'itervox',
        'external',
        'external',
      ]);
      expect(within(tabs[2]).getByText('Add health check')).toBeInTheDocument();
      expect(within(tabs[3]).getByText('svc')).toBeInTheDocument();
      expect(within(tabs[2]).getByTestId('session-tab-detail')).toHaveTextContent(
        'started outside itervox',
      );
      expect(tabs[2].className).toContain('opacity-70');
      expect(tabs[1].className).not.toContain('opacity-70');
      expect(within(tabs[3]).getByTestId('session-tab-status')).toHaveTextContent('running');
      expect(within(tabs[3]).getByTestId('session-tab-status')).toHaveAttribute(
        'title',
        'codex process 42 has the transcript open',
      );
      expect(within(tabs[2]).getByText('exited')).toBeInTheDocument();
    });

    it('shows the transcript read-only, with earlier messages on demand', async () => {
      const user = userEvent.setup();
      mocks.sessions = [HAND_EXITED];
      mocks.hasNextPage = true;
      renderAt();
      expect(screen.getByTestId('session-cwd')).toHaveTextContent('/work/app');
      const entries = screen.getAllByTestId('transcript-entry');
      expect(entries.map((e) => e.getAttribute('data-kind'))).toEqual(['text', 'tool', 'text']);
      expect(entries[0]).toHaveTextContent('Add a health check (hand-exited)');
      expect(entries[2]).toHaveTextContent('(abbreviated)');
      expect(screen.queryByTestId('terminal')).toBeNull();
      expect(screen.getByTestId('session-readonly')).toHaveTextContent('read-only');
      expect(screen.queryByRole('button', { name: 'Send' })).toBeNull();
      await user.click(screen.getByRole('button', { name: 'Load earlier messages' }));
      expect(mocks.fetchNextPage).toHaveBeenCalled();
    });

    it('shows a running hand-started session read-only, with why it counts as running', () => {
      mocks.sessions = [HAND_BUSY];
      renderAt();
      expect(screen.getByTestId('session-readonly')).toHaveTextContent('still running');
      expect(screen.getByTestId('session-running-reason')).toHaveTextContent(
        'codex process 42 has the transcript open',
      );
      expect(screen.queryByRole('textbox')).toBeNull();
      expect(screen.queryByRole('button', { name: /continue/i })).toBeNull();
    });
  });
});
