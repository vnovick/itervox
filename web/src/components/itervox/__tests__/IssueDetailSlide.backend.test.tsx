import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import IssueDetailSlide from '../IssueDetailSlide';
import { useItervoxStore } from '../../../store/itervoxStore';
import { useToastStore } from '../../../store/toastStore';
import { makeSnapshot } from '../../../test/fixtures/snapshots';
import { ISSUE_KEY, ISSUES_KEY } from '../../../queries/issues';
import type { TrackerIssue } from '../../../types/schemas';

// CORE-056 — the per-issue backend control in the issue detail slide. These
// tests run the REAL useSetIssueBackend hook (optimistic write + rollback)
// against a mocked fetch, so they exercise the wire, not a mocked mutation.

type FetchFn = (input: string, init?: RequestInit) => Promise<Response>;
let fetchMock: ReturnType<typeof vi.fn<FetchFn>>;
let backendResponse: () => Response;
let issue: TrackerIssue;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

function renderSlide(): QueryClient {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } },
  });
  queryClient.setQueryData(ISSUES_KEY, [issue]);
  queryClient.setQueryData(ISSUE_KEY(issue.identifier), issue);
  render(
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>
        <IssueDetailSlide />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  return queryClient;
}

beforeEach(() => {
  issue = {
    identifier: 'ENG-10',
    title: 'Fix the bug',
    state: 'Todo',
    orchestratorState: 'idle',
    agentBackend: 'claude',
  };
  backendResponse = () => json({ ok: true });
  fetchMock = vi.fn<FetchFn>((input, init) => {
    const url = input;
    if (url.endsWith('/backend') && init?.method === 'POST') {
      const res = backendResponse();
      // A stored pin is what the next GET returns (the daemon merges the
      // side map into every snapshot synchronously).
      if (res.ok) {
        const { backend } = JSON.parse(init.body as string) as { backend: string };
        issue = { ...issue, agentBackend: backend || undefined };
      }
      return Promise.resolve(res);
    }
    if (url.endsWith('/api/v1/issues')) return Promise.resolve(json([issue]));
    if (url.includes('/api/v1/issues/ENG-10')) return Promise.resolve(json(issue));
    return Promise.resolve(json({}));
  });
  global.fetch = fetchMock as unknown as typeof fetch;
  useToastStore.setState({ toasts: [], _timers: new Map() });
  useItervoxStore.setState({
    selectedIdentifier: 'ENG-10',
    snapshot: makeSnapshot({ defaultBackend: 'claude' }),
  });
});

afterEach(() => {
  vi.restoreAllMocks();
  useItervoxStore.setState({ selectedIdentifier: null, snapshot: null });
});

describe('IssueDetailSlide', () => {
  it('switching backend posts to /backend', async () => {
    const user = userEvent.setup();
    const qc = renderSlide();
    const select = screen.getByRole('combobox', { name: 'Agent backend' });
    expect(select).toHaveValue('claude');

    await user.selectOptions(select, 'codex');

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining('/api/v1/issues/ENG-10/backend'),
        expect.objectContaining({ method: 'POST', body: JSON.stringify({ backend: 'codex' }) }),
      );
    });
    // Optimistic write: the cached issue already shows the new backend.
    expect(qc.getQueryData<TrackerIssue>(ISSUE_KEY('ENG-10'))?.agentBackend).toBe('codex');
  });

  it('backend selector lists only known backends', () => {
    renderSlide();
    const select = screen.getByRole('combobox', { name: 'Agent backend' });
    const values = within(select)
      .getAllByRole('option')
      .map((o) => (o as HTMLOptionElement).value);
    expect(values).toEqual(['', 'claude', 'codex']);
    expect(within(select).getByRole('option', { name: 'Default (claude)' })).toBeInTheDocument();
  });

  it('unknown backend error is toasted and rolled back', async () => {
    backendResponse = () =>
      json(
        {
          error: {
            code: 'backend_pin_refused',
            message:
              'orchestrator: per-issue backend requested backend "codex" but the command runs "claude"; kept "claude"',
            field: 'backend',
          },
        },
        409,
      );
    const user = userEvent.setup();
    const qc = renderSlide();
    await user.selectOptions(screen.getByRole('combobox', { name: 'Agent backend' }), 'codex');

    await waitFor(() => {
      expect(useToastStore.getState().toasts.map((t) => t.message)).toContain(
        'orchestrator: per-issue backend requested backend "codex" but the command runs "claude"; kept "claude"',
      );
    });
    // The optimistic agentBackend is rolled back to its previous value.
    expect(qc.getQueryData<TrackerIssue>(ISSUE_KEY('ENG-10'))?.agentBackend).toBe('claude');
    expect(qc.getQueryData<TrackerIssue[]>(ISSUES_KEY)?.[0].agentBackend).toBe('claude');
    // The refusal is also shown next to the control, not only in a toast.
    expect(await screen.findByRole('alert')).toHaveTextContent('the command runs "claude"');
  });

  // BH-M3-10: the refusal (and pending state) of one issue must not leak
  // into the next issue shown in the slide.
  it('a refusal on one issue does not leak into the next issue', async () => {
    backendResponse = () =>
      json(
        { error: { code: 'backend_pin_refused', message: 'refused for ENG-10', field: 'backend' } },
        409,
      );
    const user = userEvent.setup();
    const qc = renderSlide();
    await user.selectOptions(screen.getByRole('combobox', { name: 'Agent backend' }), 'codex');
    expect(await screen.findByRole('alert')).toHaveTextContent('refused for ENG-10');

    const other: TrackerIssue = {
      identifier: 'ENG-11',
      title: 'Other',
      state: 'Todo',
      orchestratorState: 'idle',
    };
    act(() => {
      qc.setQueryData(ISSUES_KEY, [issue, other]);
      qc.setQueryData(ISSUE_KEY('ENG-11'), other);
      useItervoxStore.setState({ selectedIdentifier: 'ENG-11' });
    });
    expect(await screen.findByText('Other')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('labels a running issue "applies on next run" and marks an automatic switch', () => {
    issue = {
      ...issue,
      orchestratorState: 'running',
      agentBackend: 'codex',
      autoSwitch: {
        identifier: 'ENG-10',
        source: 'backend_fallback',
        fromBackend: 'claude',
        toBackend: 'codex',
      },
    };
    renderSlide();
    expect(screen.getByText('Applies on next run')).toBeInTheDocument();
    expect(screen.getByText(/Auto-switched from claude/)).toBeInTheDocument();
  });

  it('backend control is focusable and not disabled (keyboard reachable)', async () => {
    const user = userEvent.setup();
    renderSlide();
    const select = screen.getByRole('combobox', { name: 'Agent backend' });
    select.focus();
    expect(select).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    // jsdom does not open native pickers; selectOptions is the keyboard
    // fallback. The control must be focusable and not disabled.
    expect(select).not.toBeDisabled();
  });
});
