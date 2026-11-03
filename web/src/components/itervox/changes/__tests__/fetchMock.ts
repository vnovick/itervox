import type { vi } from 'vitest';
import type { authedFetch } from '../../../../auth/authedFetch';

/** Routes mocked authedFetch calls by URL to JSON responses. */
export function routeFetch(
  mock: ReturnType<typeof vi.mocked<typeof authedFetch>>,
  routes: Record<string, { status?: number; body: unknown } | undefined>,
) {
  mock.mockImplementation((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const hit = routes[url];
    if (!hit) return Promise.resolve(new Response('{}', { status: 500 }));
    return Promise.resolve(
      new Response(JSON.stringify(hit.body), {
        status: hit.status ?? 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
  });
}
