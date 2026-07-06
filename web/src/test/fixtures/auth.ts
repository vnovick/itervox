// Auth fixtures shared by component renders and Playwright route-mocks. The
// token value MUST match what the route-mocked Playwright config injects on
// boot so component tests and browser tests look at the same world.

export const E2E_TOKEN = 'e2e-ui-token';

export type AuthMode = 'authorized' | 'token-entry' | 'server-down';

// Browser-only — the AuthGate reads from sessionStorage / localStorage.
export function seedAuthStorage(mode: AuthMode, storage: Storage = sessionStorage): void {
  if (mode === 'authorized') {
    storage.setItem('itervox.auth.token', E2E_TOKEN);
  } else {
    storage.removeItem('itervox.auth.token');
  }
}

export function clearAuthStorage(storage: Storage = sessionStorage): void {
  storage.removeItem('itervox.auth.token');
}
