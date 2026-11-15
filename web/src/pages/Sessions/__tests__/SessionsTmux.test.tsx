import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import Sessions from '../index';
import type { NewSessionOptions, SessionRow, TmuxStatus } from '../../../types/schemas';
import type * as SessionsQueries from '../../../queries/sessions';

// #178 tmux control and New session: the banner and composer warning when
// tmux is missing, the New session form, the live screen with its
// composer and keys, and the read-only hint for sessions not in tmux.

const mocks = vi.hoisted(() => ({
  sessions: [] as SessionRow[],
  tmux: undefined as TmuxStatus | undefined,
  newSession: undefined as NewSessionOptions | undefined,
  sendMutate: vi.fn(),
  keyMutate: vi.fn(),
  startMutate: vi.fn(),
  killMutate: vi.fn(),
  screenIds: [] as string[],
}));

vi.mock('../../../queries/sessions', async (importOriginal) => ({
  ...(await importOriginal<typeof SessionsQueries>()),
  useSessions: () => ({
    sessions: mocks.sessions,
    isLoading: false,
    tmux: mocks.tmux,
    newSession: mocks.newSession,
  }),
  useSendSessionMessage: () => ({ mutate: mocks.sendMutate, isPending: false }),
  useSessionTranscript: () => ({
    entries: [],
    isLoading: false,
    isError: false,
    hasNextPage: false,
    fetchNextPage: vi.fn(),
    isFetchingNextPage: false,
  }),
}));

vi.mock('../../../queries/sessionsTmux', () => ({
  useSessionScreen: (id: string) => {
    mocks.screenIds.push(id);
    return { data: { text: `screen of ${id}\n> <b>not html</b>` }, isError: false };
  },
  useSendSessionKey: () => ({ mutate: mocks.keyMutate, isPending: false }),
  useStartSession: () => ({ mutate: mocks.startMutate, isPending: false }),
  useKillSession: () => ({ mutate: mocks.killMutate, isPending: false }),
}));

vi.mock('../../../queries/issues', () => ({
  useProvideInput: () => ({ mutate: vi.fn(), isPending: false }),
  useDismissInput: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock('../../../queries/logs', () => ({
  useIssueLogs: () => ({ data: [], isLoading: false, isError: false }),
}));

vi.mock('../../../store/itervoxStore', () => ({
  useItervoxStore: (sel: (s: unknown) => unknown) => sel({ snapshot: { inlineInput: false } }),
}));

vi.mock('../../../components/common/PageMeta', () => ({ default: () => null }));

const AVAILABLE: TmuxStatus = {
  available: true,
  version: 'tmux 3.4',
  installHint: 'brew install tmux',
  control: true,
};
const MISSING: TmuxStatus = { available: false, installHint: 'brew install tmux', control: true };
const OPTIONS: NewSessionOptions = {
  projectRoot: '/work/app',
  folders: ['/work/app', '/ws/ENG-1'],
  profiles: ['coder'],
  max: 8,
};

const HAND_IN_TMUX: SessionRow = {
  id: 'hand-1',
  owner: 'external',
  identifier: '',
  backend: 'claude',
  status: 'running',
  cwd: '/work/app',
  tmuxPane: { id: '%3', label: 'work:0.0' },
  controllable: true,
};
const HAND_PLAIN: SessionRow = {
  id: 'hand-2',
  owner: 'external',
  identifier: '',
  backend: 'claude',
  status: 'exited',
  cwd: '/work/app',
  tmuxPane: null,
  controllable: false,
};
const OWN_TMUX: SessionRow = {
  id: 'itervox-abc123',
  owner: 'itervox',
  kind: 'tmux',
  identifier: '',
  backend: 'codex',
  status: 'running',
  cwd: '/work/app',
  tmuxPane: { id: '%7', label: 'itervox-abc123:0.0' },
  controllable: true,
  attachCommand: 'tmux attach -t itervox-abc123',
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

beforeEach(() => {
  mocks.sessions = [];
  mocks.tmux = AVAILABLE;
  mocks.newSession = OPTIONS;
  mocks.sendMutate.mockReset();
  mocks.keyMutate.mockReset();
  mocks.startMutate.mockReset();
  mocks.killMutate.mockReset();
  mocks.screenIds = [];
});

describe('Sessions panel — tmux', () => {
  it('submits the New session form with the chosen agent, folder, profile and prompt', async () => {
    const user = userEvent.setup();
    renderAt();
    expect(screen.queryByTestId('new-session-form')).toBeNull();
    await user.click(screen.getByTestId('new-session-button'));
    expect(screen.getByTestId('location')).toHaveTextContent('new=1');
    const form = screen.getByTestId('new-session-form');
    await user.selectOptions(within(form).getByLabelText('Agent'), 'codex');
    await user.selectOptions(within(form).getByLabelText('Folder'), '/ws/ENG-1');
    await user.selectOptions(within(form).getByLabelText('Profile (optional)'), 'coder');
    await user.type(within(form).getByLabelText('First prompt (optional)'), '  fix the build ');
    await user.click(within(form).getByRole('button', { name: 'Start session' }));
    expect(mocks.startMutate).toHaveBeenCalledWith(
      { backend: 'codex', cwd: '/ws/ENG-1', profile: 'coder', prompt: 'fix the build' },
      expect.anything(),
    );
    // Started: the form closes on the new session's tab.
    const onSuccess = (mocks.startMutate.mock.calls[0][1] as { onSuccess: (r: SessionRow) => void })
      .onSuccess;
    onSuccess(OWN_TMUX);
    expect(await screen.findByTestId('location')).toHaveTextContent('id=itervox-abc123');
  });

  it('sends only the backend for the project folder with no profile or prompt', async () => {
    const user = userEvent.setup();
    renderAt('?new=1');
    await user.click(screen.getByRole('button', { name: 'Start session' }));
    expect(mocks.startMutate).toHaveBeenCalledWith({ backend: 'claude' }, expect.anything());
  });

  it('polls the screen of a controllable session and renders it as text', () => {
    mocks.sessions = [HAND_IN_TMUX];
    renderAt();
    const pre = screen.getByTestId('tmux-screen');
    expect(pre).toHaveTextContent('screen of hand-1');
    expect(pre.innerHTML).toContain('&lt;b&gt;not html&lt;/b&gt;');
    expect(pre.querySelector('b')).toBeNull();
    expect(mocks.screenIds).toContain('hand-1');
    expect(screen.getByTestId('session-tmux-pane')).toHaveTextContent('tmux work:0.0');
    expect(screen.getByTestId('session-badge-tmux')).toBeInTheDocument();
    expect(screen.getByTestId('session-badge-controllable')).toBeInTheDocument();
  });

  it('types text, with or without Enter, and presses keys through the right endpoints', async () => {
    const user = userEvent.setup();
    mocks.sessions = [HAND_IN_TMUX];
    renderAt();
    const box = screen.getByRole('textbox', { name: 'Text to type into the session' });
    await user.type(box, 'run the tests');
    await user.click(screen.getByRole('button', { name: 'Send' }));
    expect(mocks.sendMutate).toHaveBeenCalledWith(
      { id: 'hand-1', message: 'run the tests', submit: true },
      expect.anything(),
    );
    await user.type(box, 'draft');
    await user.click(screen.getByRole('button', { name: 'Type without Enter' }));
    expect(mocks.sendMutate).toHaveBeenLastCalledWith(
      { id: 'hand-1', message: 'run the tests' + 'draft', submit: false },
      expect.anything(),
    );
    for (const [name, key] of [
      ['Press Escape', 'Escape'],
      ['Press Enter', 'Enter'],
      ['Press Up', 'Up'],
      ['Press Down', 'Down'],
      ['Press Answer 1', '1'],
      ['Press Answer 2', '2'],
      ['Press Answer 3', '3'],
    ]) {
      await user.click(screen.getByRole('button', { name }));
      expect(mocks.keyMutate).toHaveBeenLastCalledWith({ id: 'hand-1', key });
    }
    // Ctrl-C asks first.
    mocks.keyMutate.mockReset();
    await user.click(screen.getByRole('button', { name: 'Press Ctrl-C' }));
    expect(mocks.keyMutate).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Send Ctrl-C' }));
    expect(mocks.keyMutate).toHaveBeenCalledWith({ id: 'hand-1', key: 'C-c' });
    expect(screen.queryByTestId('tmux-attach')).toBeNull();
  });

  it('shows the attach command and stops an itervox tmux session after confirming', async () => {
    const user = userEvent.setup();
    mocks.sessions = [OWN_TMUX];
    renderAt();
    expect(screen.getByTestId('tmux-attach')).toHaveTextContent('tmux attach -t itervox-abc123');
    expect(screen.getByTestId('session-tab-detail')).toHaveTextContent('started in tmux · codex');
    await user.click(screen.getByRole('button', { name: 'Stop session' }));
    expect(mocks.killMutate).not.toHaveBeenCalled();
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: 'Stop session' }));
    expect(mocks.killMutate).toHaveBeenCalledWith('itervox-abc123');
  });

  it('keeps a hand-started session not in tmux read-only, with the tmux hint', () => {
    mocks.sessions = [HAND_PLAIN];
    renderAt();
    expect(screen.getByTestId('session-readonly')).toBeInTheDocument();
    expect(screen.getByTestId('session-tmux-hint')).toHaveTextContent(
      'Run claude inside tmux and enable sessions.tmux_control to control it from here.',
    );
    expect(screen.queryByTestId('tmux-screen')).toBeNull();
    expect(screen.queryByRole('textbox')).toBeNull();
    expect(screen.queryByTestId('session-badge-tmux')).toBeNull();
  });

  it('warns in a banner and in the composer, and disables New session, when tmux is missing', async () => {
    const user = userEvent.setup();
    mocks.tmux = MISSING;
    mocks.sessions = [{ ...HAND_PLAIN, status: 'running' }];
    renderAt('?new=1');
    const banner = screen.getByTestId('tmux-missing-banner');
    expect(banner).toHaveTextContent('tmux not found — install it to control sessions from here.');
    expect(within(banner).getByTestId('tmux-install-hint')).toHaveTextContent('brew install tmux');
    const composer = screen.getByTestId('tmux-missing-composer');
    expect(composer).toHaveTextContent('tmux not found');
    expect(
      within(composer).getByRole('button', { name: 'Copy the tmux install command' }),
    ).toBeInTheDocument();
    expect(screen.queryByRole('textbox')).toBeNull();
    const button = screen.getByTestId('new-session-button');
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute(
      'title',
      'tmux not found — install it to control sessions from here.',
    );
    expect(screen.queryByTestId('new-session-form')).toBeNull();
    await user.click(button);
    expect(mocks.startMutate).not.toHaveBeenCalled();
  });

  it('copies the install command', async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    mocks.tmux = MISSING;
    renderAt();
    await user.click(screen.getByRole('button', { name: 'Copy the tmux install command' }));
    expect(writeText).toHaveBeenCalledWith('brew install tmux');
  });

  it('neither warns nor offers New session for a daemon without tmux control', () => {
    mocks.tmux = undefined;
    mocks.sessions = [HAND_PLAIN];
    renderAt();
    expect(screen.queryByTestId('tmux-missing-banner')).toBeNull();
    expect(screen.queryByTestId('new-session-button')).toBeNull();
    expect(screen.queryByTestId('session-tmux-hint')).toBeNull();
  });
});
