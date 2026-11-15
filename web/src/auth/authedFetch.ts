import { getToken, useTokenStore } from './tokenStore';
import { useAuthStore } from './authStore';
import { UnauthorizedError } from './UnauthorizedError';
import { REQUIRED_SCOPE_HEADER } from './scope';
import { apiUrl } from './apiBase';
import { useToastStore } from '../store/toastStore';

/**
 * Drop-in replacement for `fetch()` that injects `Authorization: Bearer <token>`
 * when a token is present in the token store.
 *
 * On 401:
 *   - Clears the stored token (it's invalid).
 *   - Defers `markUnauthorized()` to a microtask so TanStack Query's
 *     `onError` rollback runs BEFORE the AuthGate swaps the screen.
 *     Otherwise optimistic updates would be left dirty on the next login.
 *   - Throws `UnauthorizedError` so callers can distinguish auth failures
 *     from other non-ok responses.
 *
 * On 403 insufficient_scope (#214 — the daemon names the scope it needed in
 * `X-Itervox-Required-Scope`): raises one error toast saying which scope the
 * action needs (the toast store dedupes repeats), then returns the response
 * unchanged so the caller's own error handling still runs.
 *
 * Non-401 non-ok responses are returned unchanged — callers already check
 * `res.ok` and handle them.
 *
 * #228: a string URL written as `/api/v1/...` is mapped onto the configured
 * API base (`apiBase.ts`), so the same call works on a daemon and through
 * the hub's per-project proxy.
 */
export async function authedFetch(
  input: RequestInfo | URL,
  init: RequestInit = {},
): Promise<Response> {
  const token = getToken();
  const headers = new Headers(init.headers);
  if (token && !headers.has('Authorization')) {
    headers.set('Authorization', `Bearer ${token}`);
  }

  const res = await fetch(typeof input === 'string' ? apiUrl(input) : input, { ...init, headers });

  if (res.status === 401) {
    // Clear synchronously so subsequent requests in the same tick don't
    // retry with a known-bad token.
    useTokenStore.getState().clearToken();
    // Defer the screen swap so mutation rollbacks get a chance to run.
    queueMicrotask(() => {
      useAuthStore.getState().markUnauthorized();
    });
    throw new UnauthorizedError();
  }

  if (res.status === 403) {
    const required = requiredScope(res);
    if (required) {
      useToastStore
        .getState()
        .addToast(
          `Not allowed: this action needs the ${required} scope. Ask an admin for a token with it.`,
          'error',
        );
    }
  }

  return res;
}

/** The scope a 403 insufficient_scope response names, if any. Tolerates
 * minimal Response doubles without headers. */
function requiredScope(res: Response): string | null {
  const headers = (res as Partial<Response>).headers;
  return headers instanceof Headers ? headers.get(REQUIRED_SCOPE_HEADER) : null;
}
