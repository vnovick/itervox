import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { render } from '../../../test/render';
import { DEFAULT_MEMORY_FILTERS, useUIStore } from '../../../store/uiStore';
import { useToastStore } from '../../../store/toastStore';
import { MemoryEntrySchema, type MemoryEntry } from '../../../types/schemas';
import Memory from '../index';

vi.mock('../../../components/common/PageMeta', () => ({ default: () => null }));
vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
import { authedFetch } from '../../../auth/authedFetch';

const T = '2026-10-01T12:00:00Z';

function entry(over: Record<string, unknown>) {
  return {
    id: 'm-1',
    type: 'fact',
    text: 'This repo uses pnpm.',
    scope: 'private',
    status: 'active',
    created: T,
    updated: T,
    path: '/home/me/.itervox/memory.json',
    runs: [],
    ...over,
  };
}

const PNPM = entry({
  tags: ['tooling'],
  useCount: 2,
  lastUsed: T,
  source: { kind: 'manual', issue: 'ENG-7', links: ['https://github.com/acme/app/pull/3'] },
  runs: [{ runId: 'r1', identifier: 'ENG-9', status: 'succeeded', finishedAt: T }],
});
const STAGING = entry({
  id: 'm-2',
  type: 'decision',
  text: 'Staging is read-only on Fridays.',
  scope: 'project',
  path: '/repo/.itervox/memory.json',
  profiles: ['reviewer'],
});
const LESSON = entry({
  id: 'm-3',
  type: 'lesson',
  text: 'Rerun the SSE test once before failing.',
  status: 'proposed',
  source: { kind: 'lesson', issue: 'ENG-3' },
});

function view(entries: unknown[] = [PNPM, STAGING, LESSON]) {
  return {
    enabled: true,
    tokenBudget: 2000,
    promptShareTokens: 8000,
    schemaUrl: 'https://itervox.dev/schema/memory.schema.json',
    files: [
      { scope: 'private', path: '/home/me/.itervox/memory.json', exists: true, writable: true },
      { scope: 'project', path: '/repo/.itervox/memory.json', exists: true, writable: true },
    ],
    entries,
  };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function urlOf(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input;
  return input instanceof URL ? input.href : input.url;
}

type Handler = (url: string, init?: RequestInit) => Response | undefined;

/** Serves GET /api/v1/memory from `current()` and every write from `write`. */
function serve(current: () => unknown, write: Handler = () => undefined) {
  vi.mocked(authedFetch).mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = urlOf(input);
    const method = init?.method ?? 'GET';
    if (method === 'GET' && url === '/api/v1/memory') return Promise.resolve(json(current()));
    return Promise.resolve(write(url, init) ?? json({}, 404));
  });
}

function calls(method: string) {
  return vi
    .mocked(authedFetch)
    .mock.calls.filter(([, init]) => (init?.method ?? 'GET') === method)
    .map(([input, init]) => ({
      url: urlOf(input),
      body:
        typeof init?.body === 'string'
          ? (JSON.parse(init.body) as Record<string, unknown>)
          : undefined,
    }));
}

async function openEntry(user: ReturnType<typeof userEvent.setup>, text: string) {
  const list = await screen.findByTestId('memory-list');
  await user.click(within(list).getByText(text));
  return screen.findByTestId('memory-detail');
}

describe('Memory page (#202)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useToastStore.setState({ toasts: [] });
    useUIStore.setState({
      memoryTab: 'entries',
      memoryFilters: DEFAULT_MEMORY_FILTERS,
      memorySelectedId: null,
      memoryUndo: null,
    });
  });

  it('filters by search, type, scope, profile, tag and usage; proposed lessons stay in Lessons', async () => {
    serve(() => view());
    const user = userEvent.setup();
    render(<Memory />);
    const list = await screen.findByTestId('memory-list');
    expect(within(list).getAllByRole('button')).toHaveLength(2);
    expect(within(list).queryByText(LESSON.text)).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText('Scope'), 'project');
    expect(within(list).getAllByRole('button')).toHaveLength(1);
    expect(within(list).getByText(STAGING.text)).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText('Scope'), 'all');

    await user.selectOptions(screen.getByLabelText('Type'), 'fact');
    expect(within(list).getByText(PNPM.text)).toBeInTheDocument();
    expect(within(list).queryByText(STAGING.text)).not.toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText('Type'), 'all');

    await user.selectOptions(screen.getByLabelText('Tag'), 'tooling');
    expect(within(list).getAllByRole('button')).toHaveLength(1);
    await user.selectOptions(screen.getByLabelText('Tag'), '');

    await user.selectOptions(screen.getByLabelText('Usage'), 'unused');
    expect(within(list).getByText(STAGING.text)).toBeInTheDocument();
    expect(within(list).queryByText(PNPM.text)).not.toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText('Usage'), 'all');

    await user.type(screen.getByLabelText('Search'), 'eng-7');
    expect(within(list).getAllByRole('button')).toHaveLength(1);
    expect(within(list).getByText(PNPM.text)).toBeInTheDocument();
    expect(useUIStore.getState().memoryFilters.search).toBe('eng-7');

    await user.clear(screen.getByLabelText('Search'));
    await user.type(screen.getByLabelText('Search'), 'nothing like this');
    expect(await screen.findByTestId('memory-empty')).toHaveTextContent('No entry matches');

    const lessons = screen.getByRole('tab', { name: /Lessons/ });
    expect(lessons).toHaveTextContent('1');
    await user.click(lessons);
    expect(await screen.findByTestId('lessons-list')).toHaveTextContent(LESSON.text);
    expect(screen.getByTestId('lessons-list')).toHaveTextContent('not used until approved');
  });

  it('shows provenance and the runs that used an entry, and the file path to open', async () => {
    serve(() => view());
    const user = userEvent.setup();
    render(<Memory />);
    const detail = await openEntry(user, PNPM.text);
    expect(within(detail).getByText('ENG-7')).toBeInTheDocument();
    expect(
      within(detail).getByRole('link', { name: 'https://github.com/acme/app/pull/3' }),
    ).toBeInTheDocument();
    expect(within(detail).getByText('ENG-9')).toBeInTheDocument();
    expect(within(detail).getByText('/home/me/.itervox/memory.json')).toBeInTheDocument();
    expect(
      within(detail).getByRole('button', { name: 'Copy the memory file path' }),
    ).toBeInTheDocument();
  });

  it('edits an entry inline and saves a partial update', async () => {
    let entries = [PNPM, STAGING];
    serve(
      () => view(entries),
      (url, init) => {
        if (init?.method !== 'PUT') return undefined;
        const body = JSON.parse(init.body as string) as Record<string, unknown>;
        const updated = { ...PNPM, ...body };
        entries = [updated, STAGING];
        expect(url).toBe('/api/v1/memory/m-1');
        return json(updated);
      },
    );
    const user = userEvent.setup();
    render(<Memory />);
    const detail = await openEntry(user, PNPM.text);
    await user.click(within(detail).getByRole('button', { name: 'Edit' }));
    const text = within(detail).getByLabelText('Text');
    await user.clear(text);
    await user.type(text, 'This repo uses pnpm 9.');
    await user.type(within(detail).getByLabelText('Profiles'), 'implementer');
    await user.click(within(detail).getByRole('button', { name: 'Save' }));
    await waitFor(() => {
      expect(calls('PUT')[0]?.body).toEqual({
        text: 'This repo uses pnpm 9.',
        type: 'fact',
        tags: ['tooling'],
        profiles: ['implementer'],
      });
    });
    expect(await screen.findAllByText('This repo uses pnpm 9.')).not.toHaveLength(0);
  });

  it('pins and archives through their endpoints', async () => {
    serve(
      () => view(),
      (url, init) => {
        if (url.endsWith('/pin')) return json({ ...PNPM, pinned: true });
        if (init?.method === 'PUT') return json({ ...PNPM, status: 'archived' });
        return undefined;
      },
    );
    const user = userEvent.setup();
    render(<Memory />);
    const detail = await openEntry(user, PNPM.text);
    await user.click(within(detail).getByRole('button', { name: 'Pin' }));
    await waitFor(() => {
      expect(calls('POST')).toContainEqual({
        url: '/api/v1/memory/m-1/pin',
        body: { pinned: true },
      });
    });
    await user.click(within(detail).getByRole('button', { name: 'Archive' }));
    await waitFor(() => {
      expect(calls('PUT')).toContainEqual({
        url: '/api/v1/memory/m-1',
        body: { status: 'archived' },
      });
    });
  });

  it('deletes with undo: the entry leaves the list and Undo posts it back unchanged', async () => {
    let entries: unknown[] = [PNPM, STAGING];
    serve(
      () => view(entries),
      (url, init) => {
        if (init?.method === 'DELETE') {
          entries = [STAGING];
          return json(PNPM);
        }
        if (init?.method === 'POST' && url === '/api/v1/memory') {
          entries = [PNPM, STAGING];
          return json(PNPM, 201);
        }
        return undefined;
      },
    );
    const user = userEvent.setup();
    render(<Memory />);
    const detail = await openEntry(user, PNPM.text);
    await user.click(within(detail).getByRole('button', { name: 'Delete' }));
    await waitFor(() => {
      expect(
        within(screen.getByTestId('memory-list')).queryByText(PNPM.text),
      ).not.toBeInTheDocument();
    });
    expect(calls('DELETE')[0]?.url).toBe('/api/v1/memory/m-1');

    await user.click(await screen.findByRole('button', { name: 'Undo' }));
    await waitFor(() => {
      expect(calls('POST')).toHaveLength(1);
    });
    const restored = calls('POST')[0];
    expect(restored.url).toBe('/api/v1/memory');
    const sent: Partial<MemoryEntry> = MemoryEntrySchema.parse(PNPM);
    delete sent.path;
    delete sent.runs;
    expect(restored.body).toEqual(sent);
    expect(await screen.findByText(PNPM.text)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument();
  });

  it('rolls an optimistic delete back when the server refuses it', async () => {
    serve(
      () => view(),
      (_url, init) =>
        init?.method === 'DELETE'
          ? json({ error: { code: 'memory_invalid', message: 'fix the file first' } }, 400)
          : undefined,
    );
    // Only the rollback may bring the entry back: the refetch after the
    // failed delete never answers.
    const first = vi.mocked(authedFetch).getMockImplementation();
    let gets = 0;
    vi.mocked(authedFetch).mockImplementation((input, init) => {
      if ((init?.method ?? 'GET') === 'GET' && ++gets > 1) return new Promise<Response>(() => {});
      if (!first) throw new Error('no handler');
      return first(input, init);
    });
    const user = userEvent.setup();
    render(<Memory />);
    const detail = await openEntry(user, PNPM.text);
    await user.click(within(detail).getByRole('button', { name: 'Delete' }));
    await waitFor(() => {
      expect(
        useToastStore
          .getState()
          .toasts.map((t) => t.message)
          .join(' '),
      ).toContain('Memory not deleted');
    });
    expect(within(screen.getByTestId('memory-list')).getByText(PNPM.text)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument();
  });

  it('promotes a private entry to the project and shows the git diff', async () => {
    const diff =
      'diff --git a/.itervox/memory.json b/.itervox/memory.json\n+      "text": "This repo uses pnpm."';
    serve(
      () => view(),
      (url) =>
        url === '/api/v1/memory/m-1/promote'
          ? json({
              entry: { ...PNPM, scope: 'project', path: '/repo/.itervox/memory.json' },
              path: '/repo/.itervox/memory.json',
              diff,
            })
          : undefined,
    );
    const user = userEvent.setup();
    render(<Memory />);
    const detail = await openEntry(user, PNPM.text);
    await user.click(within(detail).getByRole('button', { name: 'Share with the project' }));
    expect(await within(detail).findByTestId('memory-promote-diff')).toHaveTextContent(
      'diff --git',
    );
    expect(calls('POST')[0]?.url).toBe('/api/v1/memory/m-1/promote');
    await waitFor(() => {
      expect(useToastStore.getState().toasts.map((t) => t.message)).toContain(
        'Moved to the project memory',
      );
    });
  });

  it('adds an entry, refusing text over the size cap', async () => {
    serve(
      () => view([]),
      (url, init) =>
        init?.method === 'POST' && url === '/api/v1/memory' ? json(PNPM, 201) : undefined,
    );
    const user = userEvent.setup();
    render(<Memory />);
    expect(await screen.findByTestId('memory-empty')).toHaveTextContent('No memory yet');
    await user.click(screen.getByRole('button', { name: 'Add memory' }));
    const form = screen.getByRole('form', { name: 'New memory' });
    const text = within(form).getByLabelText('What should every run remember?');
    await user.click(text);
    await user.paste('x'.repeat(2001));
    expect(within(form).getByRole('alert')).toHaveTextContent('at most 2000');
    expect(within(form).getByRole('button', { name: 'Save' })).toBeDisabled();

    await user.clear(text);
    await user.type(text, 'Use pnpm');
    await user.type(within(form).getByLabelText('Tags'), 'tooling, web');
    await user.click(within(form).getByRole('button', { name: 'Save' }));
    await waitFor(() => {
      expect(calls('POST')[0]?.body).toEqual({
        text: 'Use pnpm',
        type: 'fact',
        scope: 'private',
        tags: ['tooling', 'web'],
      });
    });
  });

  it('says so when memory is off', async () => {
    serve(() => ({ ...view(), enabled: false }));
    render(<Memory />);
    expect(await screen.findByText(/Memory is off/)).toBeInTheDocument();
  });
});
