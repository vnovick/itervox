import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router';
import MobileIssuePage from '../index';
import { configureApiContext } from '../../../auth/apiBase';
import { render } from '../../../test/render';
import { makeSnapshot } from '../../../test/fixtures/snapshots';

// #231 — one issue on a phone through the hub: approve / reject a guardrail
// approval, reply to a question, steer and pause, all against the project's
// proxied API.

function context(run: Record<string, unknown>) {
  return {
    identifier: 'ENG-1',
    run: { runId: 'run-1', live: true, toolCalls: 0, turnCount: 3, ...run },
    changedFiles: [],
    handoffs: [],
  };
}

function mockFetch(ctx: unknown) {
  const calls: { url: string; method: string; body: unknown }[] = [];
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET';
    calls.push({
      url,
      method,
      body: typeof init?.body === 'string' ? (JSON.parse(init.body) as unknown) : undefined,
    });
    if (url.endsWith('/context')) {
      return Promise.resolve(new Response(JSON.stringify(ctx), { status: 200 }));
    }
    if (url.endsWith('/steering/phrases')) {
      return Promise.resolve(
        new Response(
          JSON.stringify({ phrases: [{ label: 'Run the tests', message: 'Run the tests now.' }] }),
          { status: 200 },
        ),
      );
    }
    if (method === 'POST') return Promise.resolve(new Response('{}', { status: 202 }));
    return Promise.resolve(new Response('{}', { status: 200 }));
  });
  global.fetch = fn as unknown as typeof fetch;
  return calls;
}

function renderIssue() {
  return render(
    <Routes>
      <Route path="/m/:identifier" element={<MobileIssuePage />} />
    </Routes>,
    {
      route: '/m/ENG-1',
      snapshot: makeSnapshot({ callerScope: 'operator', capabilities: ['dispatch_pause'] }),
    },
  );
}

beforeEach(() => {
  configureApiContext('/hub/p/alpha/m/ENG-1');
});
afterEach(() => {
  configureApiContext('/');
  vi.restoreAllMocks();
});

describe('MobileIssuePage', () => {
  it('approves a guardrail request through the project proxy', async () => {
    const calls = mockFetch(
      context({ pendingInput: 'Run `vercel --prod`?', pendingApproval: true }),
    );
    renderIssue();
    const section = await screen.findByRole('region', { name: 'Approval' });
    expect(within(section).getByText('Run `vercel --prod`?')).toBeTruthy();
    expect(within(section).queryByRole('textbox')).toBeNull();
    fireEvent.click(within(section).getByRole('button', { name: 'Approve' }));
    await waitFor(() => {
      expect(calls.some((c) => c.method === 'POST')).toBe(true);
    });
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.url).toBe('/hub/p/alpha/api/v1/issues/ENG-1/provide-input');
    expect(post?.body).toEqual({ message: 'approve' });
    expect(screen.getByText('alpha')).toBeTruthy();
  });

  it('rejects a guardrail request', async () => {
    const calls = mockFetch(context({ pendingInput: 'Delete the branch?', pendingApproval: true }));
    renderIssue();
    fireEvent.click(await screen.findByRole('button', { name: 'Reject' }));
    await waitFor(() => {
      expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ message: 'reject' });
    });
  });

  it('replies to a question with the shared reply box', async () => {
    const calls = mockFetch(context({ pendingInput: 'Which database?' }));
    renderIssue();
    const section = await screen.findByRole('region', { name: 'Reply' });
    expect(within(section).queryByRole('button', { name: 'Approve' })).toBeNull();
    fireEvent.change(within(section).getByRole('textbox', { name: 'Reply to the agent' }), {
      target: { value: 'Use Postgres' },
    });
    fireEvent.click(within(section).getByRole('button', { name: 'Reply & Resume Agent' }));
    await waitFor(() => {
      const post = calls.find((c) => c.method === 'POST');
      expect(post?.url).toBe('/hub/p/alpha/api/v1/issues/ENG-1/provide-input');
      expect(post?.body).toEqual({ message: 'Use Postgres' });
    });
  });

  it('shows the run now, steering chips and the dispatch pause for a running issue', async () => {
    const calls = mockFetch(context({ currentStep: 'Running the test suite' }));
    renderIssue();
    expect(await screen.findByText('Running the test suite')).toBeTruthy();
    expect(screen.queryByRole('region', { name: 'Reply' })).toBeNull();
    fireEvent.click(await screen.findByRole('button', { name: 'Run the tests' }));
    await waitFor(() => {
      const post = calls.find((c) => c.method === 'POST');
      expect(post?.url).toBe('/hub/p/alpha/api/v1/sessions/run-1/message');
      expect(post?.body).toEqual({ message: 'Run the tests now.' });
    });
    expect(screen.getByTestId('dispatch-pause-toggle').textContent).toBe('Pause dispatch');
    expect(screen.getByRole('link', { name: 'Back to the hub' }).getAttribute('href')).toBe(
      '/hub/m',
    );
  });
});
