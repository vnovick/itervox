// CORE-095 — the command palette.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { useState } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { CommandPalette } from '../CommandPalette';
import { useItervoxStore } from '../../../store/itervoxStore';
import { useUIStore } from '../../../store/uiStore';
import { ThemeProvider } from '../../../context/ThemeContext';
import { makeTestQueryClient } from '../../../test/queryClient';
import { makeRunningRow, makeSnapshot } from '../../../test/fixtures/snapshots';
import { RECENTS_KEY } from '../palette/recents';
import { useReviewDraftStore } from '../../../store/reviewDraftStore';
import { ISSUE_DIFF_KEY } from '../../../queries/changes';

const mocks = vi.hoisted(() => ({ terminate: vi.fn(), cancel: vi.fn(), sendReview: vi.fn() }));
vi.mock('../../../queries/issues', () => ({
  useIssues: () => ({
    data: [
      { identifier: 'ENG-1', title: 'Fix the login', state: 'In Progress' },
      { identifier: 'ENG-2', title: 'Write docs', state: 'Todo' },
    ],
  }),
  useTerminateIssue: () => ({ mutate: mocks.terminate, isPending: false }),
  useCancelIssue: () => ({ mutate: mocks.cancel, isPending: false }),
}));
vi.mock('../../../queries/review', () => ({
  useSendReviewToAgent: () => ({ mutateAsync: mocks.sendReview, isPending: false }),
}));
// The heavier sources, read on open (palette/usePaletteSources).
vi.mock('../../../queries/memory', () => ({
  useMemory: () => ({
    data: {
      entries: [{ id: 'm-1', type: 'fact', text: 'This repo uses pnpm.', tags: [], profiles: [] }],
    },
  }),
}));
vi.mock('../../../queries/rules', () => ({
  useRules: () => ({
    data: {
      layers: [
        {
          name: 'project',
          rules: [{ id: 'small-prs', severity: 'should', text: 'Keep PRs small.' }],
        },
      ],
    },
  }),
}));
vi.mock('../../../queries/sessions', () => ({
  sessionKey: (s: { id: string }) => s.id,
  sessionLabel: (s: { identifier: string }) => s.identifier,
  useSessions: () => ({
    sessions: [{ id: 'sess-1', owner: 'itervox', identifier: 'ENG-1', status: 'input_required' }],
  }),
}));
vi.mock('../../../queries/settingsLayers', () => ({
  useSettingsSchema: () => ({
    data: {
      properties: {
        agent: {
          type: 'object',
          properties: { max_turns: { type: 'integer', title: 'Max turns' } },
        },
      },
    },
  }),
}));

function Where() {
  const loc = useLocation();
  return <p data-testid="where">{`${loc.pathname}${loc.search}${loc.hash}`}</p>;
}

function Harness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        onClick={() => {
          setOpen(true);
        }}
      >
        Open palette
      </button>
      <CommandPalette
        isOpen={open}
        onClose={() => {
          setOpen(false);
        }}
      />
    </>
  );
}

beforeEach(() => {
  mocks.terminate.mockReset();
  mocks.cancel.mockReset();
  window.localStorage.removeItem(RECENTS_KEY);
  useItervoxStore.setState({
    snapshot: makeSnapshot({
      running: [makeRunningRow({ identifier: 'ENG-1' })],
      profileDefs: { coder: { command: 'claude', backend: 'claude' } },
      automations: [
        { id: 'nightly-qa', enabled: true, profile: 'coder', trigger: { type: 'cron' } },
      ],
    }),
    selectedIdentifier: null,
  });
});

function renderPalette(client = makeTestQueryClient()) {
  return render(
    <QueryClientProvider client={client}>
      <ThemeProvider>
        <MemoryRouter>
          <Harness />
          <Where />
        </MemoryRouter>
      </ThemeProvider>
    </QueryClientProvider>,
  );
}

async function openAndType(text: string, client?: QueryClient) {
  const user = userEvent.setup();
  renderPalette(client);
  await user.click(screen.getByRole('button', { name: 'Open palette' }));
  const box = await screen.findByRole('combobox');
  if (text) await user.type(box, text);
  return { user, box };
}

const optionNames = () => screen.getAllByRole('option').map((o) => o.textContent);

describe('CommandPalette', () => {
  it('Escape closes and restores focus to the opener', async () => {
    const user = userEvent.setup();
    renderPalette();
    const opener = screen.getByRole('button', { name: 'Open palette' });
    await user.click(opener);
    const input = await screen.findByRole('combobox', { name: /search commands/i });
    await waitFor(() => {
      expect(input).toHaveFocus();
    });
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog', { name: /command palette/i })).not.toBeInTheDocument();
    await waitFor(() => {
      expect(opener).toHaveFocus();
    });
  });

  it('jumps to an issue: typing its id and Enter selects it', async () => {
    const user = userEvent.setup();
    renderPalette();
    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    await user.type(await screen.findByRole('combobox'), 'ENG-2');
    await user.keyboard('{Enter}');
    expect(useItervoxStore.getState().selectedIdentifier).toBe('ENG-2');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('terminate requires a confirm step before mutate is called', async () => {
    const user = userEvent.setup();
    renderPalette();
    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    await user.type(await screen.findByRole('combobox'), 'discard eng-1');
    await user.keyboard('{Enter}');
    expect(mocks.terminate).not.toHaveBeenCalled();
    const confirm = screen.getByRole('button', { name: /yes, discard ENG-1/i });
    expect(confirm).toHaveFocus();
    // The confirm step keeps the palette chrome: the chosen command in the
    // header row and a "Confirm step" eyebrow over the prompt.
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('Confirm step')).toHaveClass('eyebrow');
    expect(within(dialog).getAllByText(/^Discard ENG-1/).length).toBeGreaterThan(0);
    await user.click(confirm);
    expect(mocks.terminate).toHaveBeenCalledWith('ENG-1');
  });

  it('pause also requires a confirm step, and Cancel backs out', async () => {
    const user = userEvent.setup();
    renderPalette();
    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    await user.type(await screen.findByRole('combobox'), 'pause eng-1');
    await user.keyboard('{Enter}');
    await user.click(screen.getByRole('button', { name: /^cancel$/i }));
    expect(mocks.cancel).not.toHaveBeenCalled();
    expect(screen.getByRole('combobox')).toBeInTheDocument();
  });

  it('arrow keys move the active option (aria-activedescendant)', async () => {
    const user = userEvent.setup();
    renderPalette();
    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    const input = await screen.findByRole('combobox');
    const first = input.getAttribute('aria-activedescendant');
    await user.keyboard('{ArrowDown}');
    expect(input.getAttribute('aria-activedescendant')).not.toBe(first);
    act(() => undefined);
  });

  it('a click on an option runs it; ArrowUp moves back; no-match shows a message', async () => {
    const user = userEvent.setup();
    renderPalette();
    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    const input = await screen.findByRole('combobox');
    await user.keyboard('{ArrowDown}{ArrowDown}{ArrowUp}');
    const active = input.getAttribute('aria-activedescendant');
    expect(document.getElementById(active ?? '')).toHaveAttribute('aria-selected', 'true');
    await user.type(input, 'zzzz-nothing');
    expect(screen.getByText('No matching commands')).toBeInTheDocument();
    await user.clear(input);
    await user.type(input, 'ENG-1 Fix');
    await user.click(screen.getByRole('option', { name: /Open ENG-1/ }));
    expect(useItervoxStore.getState().selectedIdentifier).toBe('ENG-1');
  });

  it('empty query: the top actions; after running a command it is listed first as Recent', async () => {
    const { user, box } = await openAndType('');
    expect(optionNames()[0]).toMatch(/New task/);
    expect(screen.queryByText('Recent')).not.toBeInTheDocument();
    await user.type(box, '/logs');
    await user.keyboard('{Enter}');
    expect(screen.getByTestId('where')).toHaveTextContent('/logs');
    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    await screen.findByRole('combobox');
    expect(screen.getByText('Recent')).toBeInTheDocument();
    expect(optionNames()[0]).toMatch(/Go to Logs/);
  });

  it('fuzzy: "eng2" finds ENG-2 first', async () => {
    const { user } = await openAndType('eng2');
    expect(optionNames()[0]).toMatch(/Open ENG-2/);
    await user.keyboard('{Enter}');
    expect(useItervoxStore.getState().selectedIdentifier).toBe('ENG-2');
  });

  it('? lists the prefixes; picking one fills it in and narrows the list', async () => {
    const { user, box } = await openAndType('?');
    expect(optionNames()).toEqual([
      expect.stringContaining('Actions'),
      expect.stringContaining('Issues'),
      expect.stringContaining('Agents and profiles'),
      expect.stringContaining('Pages'),
      expect.stringContaining('Help'),
    ]);
    await user.click(screen.getByRole('option', { name: /Agents and profiles/ }));
    expect(box).toHaveValue('@');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(optionNames().every((n) => n.startsWith('@'))).toBe(true);
  });

  it('@ narrows to profiles and opens one in Agents', async () => {
    const { user } = await openAndType('@cod');
    expect(optionNames()).toEqual([
      expect.stringMatching(/Open coder in Agents/),
      expect.stringMatching(/Edit profile coder/),
    ]);
    await user.keyboard('{Enter}');
    expect(screen.getByTestId('where')).toHaveTextContent('/agents#profile-coder');
  });

  it('# narrows to issues (and the search-logs row)', async () => {
    await openAndType('#eng');
    const names = optionNames();
    expect(names.slice(0, 2)).toEqual([
      expect.stringMatching(/Open ENG-1/),
      expect.stringMatching(/Open ENG-2/),
    ]);
    expect(names[names.length - 1]).toMatch(/Search logs for/);
    expect(names.some((n) => n.includes('Discard'))).toBe(false);
  });

  it('> narrows to actions and running work', async () => {
    await openAndType('>');
    const names = optionNames();
    expect(names[0]).toMatch(/New task/);
    expect(names.some((n) => n.includes('Discard ENG-1'))).toBe(true);
    expect(names.some((n) => n.includes('Go to'))).toBe(false);
  });

  it('a settings field jumps to /settings?setting=<key>', async () => {
    const { user } = await openAndType('max turns');
    expect(optionNames()[0]).toMatch(/Max turns/);
    await user.keyboard('{Enter}');
    expect(screen.getByTestId('where')).toHaveTextContent('/settings?setting=agent.max_turns');
  });

  it('finds memory, rules, sessions and automations', async () => {
    const { user, box } = await openAndType('pnpm');
    await user.keyboard('{Enter}');
    expect(screen.getByTestId('where')).toHaveTextContent('/memory');
    expect(useUIStore.getState().memorySelectedId).toBe('m-1');

    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    await user.type(await screen.findByRole('combobox'), 'small-prs');
    await user.keyboard('{Enter}');
    expect(screen.getByTestId('where')).toHaveTextContent('/rules');
    expect(useUIStore.getState().rulesLayer).toBe('project');

    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    await user.type(await screen.findByRole('combobox'), 'nightly');
    await user.keyboard('{Enter}');
    expect(screen.getByTestId('where')).toHaveTextContent('/automations?openAutomation=nightly-qa');

    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    const again = await screen.findByRole('combobox');
    await user.type(again, 'input required');
    expect(screen.getByRole('option', { name: /ENG-1.*input required/ })).toBeInTheDocument();
    expect(box).not.toBeInTheDocument();
  });

  it('switch theme runs at once', async () => {
    const before = document.documentElement.getAttribute('data-theme');
    const { user } = await openAndType('switch theme');
    await user.keyboard('{Enter}');
    await waitFor(() => {
      expect(document.documentElement.getAttribute('data-theme')).not.toBe(before);
    });
  });

  it('pause dispatch is offered with the capability and asks first', async () => {
    useItervoxStore.setState((s) => ({
      snapshot: s.snapshot && { ...s.snapshot, capabilities: ['dispatch_pause'] },
    }));
    const { user } = await openAndType('>pause disp');
    expect(optionNames()[0]).toMatch(/Pause dispatch/);
    await user.keyboard('{Enter}');
    expect(screen.getByRole('button', { name: 'Pause dispatch' })).toHaveFocus();
    expect(within(screen.getByRole('dialog')).getByText(/Running agents continue/)).toBeVisible();
  });

  it('a viewer gets no operator actions and no settings fields', async () => {
    useItervoxStore.setState((s) => ({
      snapshot: s.snapshot && {
        ...s.snapshot,
        callerScope: 'viewer',
        capabilities: ['dispatch_pause'],
      },
    }));
    await openAndType('>');
    const names = optionNames();
    expect(names.some((n) => n.includes('New task'))).toBe(false);
    expect(names.some((n) => n.includes('Pause dispatch'))).toBe(false);
    expect(names.some((n) => n.includes('Refresh models'))).toBe(false);
  });

  it('with an issue open: its PR (from the cached diff) and its draft review', async () => {
    mocks.sendReview.mockResolvedValue({});
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    const client = new QueryClient({ defaultOptions: { queries: { gcTime: Infinity } } });
    client.setQueryData(ISSUE_DIFF_KEY('ENG-1'), {
      identifier: 'ENG-1',
      files: [],
      prUrl: 'https://github.example/o/r/pull/7',
    });
    useItervoxStore.setState({ selectedIdentifier: 'ENG-1' });
    useReviewDraftStore
      .getState()
      .add('ENG-1', { path: 'a.go', line: 3, side: 'RIGHT', body: 'Rename' });
    const { user } = await openAndType('', client);
    expect(screen.getByRole('option', { name: /Open ENG-1’s pull request/ })).toBeInTheDocument();
    await user.type(screen.getByRole('combobox'), 'send review');
    await user.keyboard('{Enter}');
    await user.click(screen.getByRole('button', { name: 'Send review' }));
    expect(mocks.sendReview).toHaveBeenCalledWith(
      expect.objectContaining({ moveToActive: true, alsoPostOnPR: true, note: '' }),
    );
    await waitFor(() => {
      expect(useReviewDraftStore.getState().pending['ENG-1']).toBeUndefined();
    });
    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    await user.type(await screen.findByRole('combobox'), 'pull request');
    await user.keyboard('{Enter}');
    expect(open).toHaveBeenCalledWith(
      'https://github.example/o/r/pull/7',
      '_blank',
      'noopener,noreferrer',
    );
    open.mockRestore();
  });
});
