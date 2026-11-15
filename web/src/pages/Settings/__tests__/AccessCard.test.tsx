import { render as rtlRender, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactElement, ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));

import { authedFetch } from '../../../auth/authedFetch';
import { useToastStore } from '../../../store/toastStore';
import { AccessCard } from '../AccessCard';

// #214 — Settings → Access: list, create, rotate and revoke stored tokens.

const mockFetch = vi.mocked(authedFetch);

const TOKENS = [
  {
    id: 'tok_aaaaaaaaaaaa',
    name: 'teammate',
    scope: 'viewer',
    created: '2026-10-01T10:00:00Z',
    lastUsed: '2026-10-20T09:00:00Z',
  },
  {
    id: 'tok_bbbbbbbbbbbb',
    name: 'ci',
    scope: 'operator',
    created: '2026-09-01T10:00:00Z',
    expires: '2026-12-01T00:00:00Z',
  },
];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function render(ui: ReactElement) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
  return rtlRender(ui, { wrapper });
}

/** Routes authedFetch by method + path. */
function route(handlers: Partial<Record<string, () => Response | Promise<Response>>>) {
  mockFetch.mockImplementation((input, init) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const key = `${init?.method ?? 'GET'} ${url}`;
    const handler = handlers[key];
    if (!handler) throw new Error(`unexpected ${key}`);
    return Promise.resolve(handler());
  });
}

beforeEach(() => {
  mockFetch.mockReset();
  useToastStore.setState({ toasts: [] });
});

describe('AccessCard', () => {
  it('lists the stored tokens with scope, dates and expiry', async () => {
    route({ 'GET /api/v1/tokens': () => json({ tokens: TOKENS }) });
    render(<AccessCard />);

    const rows = await screen.findAllByTestId('access-token-row');
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText('teammate')).toBeInTheDocument();
    expect(within(rows[0]).getByText('viewer')).toBeInTheDocument();
    expect(within(rows[0]).getByText('2026-10-20')).toBeInTheDocument();
    expect(within(rows[0]).getByText('never')).toBeInTheDocument();
    expect(within(rows[1]).getByText('2026-12-01')).toBeInTheDocument();
  });

  it('creates a token and shows its secret once', async () => {
    const user = userEvent.setup();
    let created = false;
    route({
      'GET /api/v1/tokens': () => json({ tokens: created ? TOKENS : [] }),
      'POST /api/v1/tokens': () => {
        created = true;
        return json(
          { token: { ...TOKENS[1], name: 'ci' }, secret: 'itx_secretvalue0123456789' },
          201,
        );
      },
    });
    render(<AccessCard />);
    expect(await screen.findByText('No stored tokens yet.')).toBeInTheDocument();

    await user.type(screen.getByPlaceholderText(/teammate laptop/), 'ci');
    await user.selectOptions(screen.getByRole('combobox'), 'operator');
    await user.type(screen.getByPlaceholderText(/never/), '90d');
    await user.click(screen.getByRole('button', { name: 'Create token' }));

    const shown = await screen.findByTestId('access-new-secret');
    expect(shown).toHaveTextContent('itx_secretvalue0123456789');
    expect(shown).toHaveTextContent('shown only once');
    const post = mockFetch.mock.calls.find(([, init]) => init?.method === 'POST');
    const body = post?.[1]?.body;
    expect(JSON.parse(typeof body === 'string' ? body : '')).toEqual({
      name: 'ci',
      scope: 'operator',
      expires: '90d',
    });

    await user.click(within(shown).getByRole('button', { name: 'Done' }));
    expect(screen.queryByTestId('access-new-secret')).not.toBeInTheDocument();
    expect(screen.queryByText('itx_secretvalue0123456789')).not.toBeInTheDocument();
  });

  it('rotates a token after confirmation and shows the new secret', async () => {
    const user = userEvent.setup();
    route({
      'GET /api/v1/tokens': () => json({ tokens: TOKENS }),
      'POST /api/v1/tokens/tok_aaaaaaaaaaaa/rotate': () =>
        json({ token: TOKENS[0], secret: 'itx_rotatedsecret987654321' }),
    });
    render(<AccessCard />);
    await user.click(await screen.findByRole('button', { name: 'Rotate teammate' }));
    await user.click(screen.getByRole('button', { name: 'Yes, rotate' }));
    expect(await screen.findByTestId('access-new-secret')).toHaveTextContent(
      'itx_rotatedsecret987654321',
    );
  });

  it('revokes a token after confirmation and rolls back with a toast on failure', async () => {
    const user = userEvent.setup();
    let lists = 0;
    route({
      // The refetch after the failure never answers, so only the rollback
      // can bring the row back.
      'GET /api/v1/tokens': () =>
        lists++ === 0 ? json({ tokens: TOKENS }) : new Promise<Response>(() => {}),
      'DELETE /api/v1/tokens/tok_bbbbbbbbbbbb': () =>
        json({ error: { code: 'token_update_failed', message: 'disk full' } }, 500),
    });
    render(<AccessCard />);
    await user.click(await screen.findByRole('button', { name: 'Revoke ci' }));
    await user.click(screen.getByRole('button', { name: 'Yes, revoke' }));

    await waitFor(() => {
      expect(useToastStore.getState().toasts[0]?.message).toContain('Revoking the token failed');
    });
    expect(await screen.findAllByTestId('access-token-row')).toHaveLength(2);
  });

  it('revokes a token', async () => {
    const user = userEvent.setup();
    let revoked = false;
    route({
      'GET /api/v1/tokens': () => json({ tokens: revoked ? [TOKENS[0]] : TOKENS }),
      'DELETE /api/v1/tokens/tok_bbbbbbbbbbbb': () => {
        revoked = true;
        return json({ revoked: TOKENS[1] });
      },
    });
    render(<AccessCard />);
    await user.click(await screen.findByRole('button', { name: 'Revoke ci' }));
    await user.click(screen.getByRole('button', { name: 'Yes, revoke' }));
    await waitFor(() => {
      expect(screen.getAllByTestId('access-token-row')).toHaveLength(1);
    });
  });
});
