// Single render helper for component tests. Mounts a UI under MemoryRouter +
// QueryClientProvider + AuthGate, with stores reset to a known initial state
// and the auth status pre-seeded so AuthGate doesn't trigger a network probe.

import { QueryClientProvider } from '@tanstack/react-query';
import { render as rtlRender, type RenderOptions, type RenderResult } from '@testing-library/react';
import type { ReactElement, ReactNode } from 'react';
import { MemoryRouter } from 'react-router';
import { AuthGate } from '../auth/AuthGate';
import { useAuthStore } from '../auth/authStore';
import { useTokenStore } from '../auth/tokenStore';
import { useItervoxStore } from '../store/itervoxStore';
import type { StateSnapshot } from '../types/schemas';
import { E2E_TOKEN, type AuthMode } from './fixtures/auth';
import { quickstartScenario } from './fixtures/scenarios';
import { makeTestQueryClient } from './queryClient';
import { resetAllStores } from './resetStores';

export interface ItervoxRenderOptions extends Omit<RenderOptions, 'wrapper'> {
  /** Initial route for the MemoryRouter. Defaults to "/". */
  route?: string;
  /** Snapshot to seed in the itervox store. Pass null to leave the store empty. Defaults to `quickstartScenario.snapshot`. */
  snapshot?: StateSnapshot | null;
  /** Initial AuthGate mode. Defaults to "authorized" with E2E_TOKEN. */
  auth?: AuthMode;
}

export function render(ui: ReactElement, options: ItervoxRenderOptions = {}): RenderResult {
  const {
    route = '/',
    snapshot = quickstartScenario.snapshot,
    auth = 'authorized',
    ...rtlOptions
  } = options;

  resetAllStores();

  if (snapshot) {
    useItervoxStore.setState({ snapshot });
  }

  // Pre-seed AuthGate state. AuthGate only triggers a probe when status is
  // 'unknown', so any other initial value avoids the network call entirely.
  switch (auth) {
    case 'authorized':
      useTokenStore.setState({ token: E2E_TOKEN });
      useAuthStore.setState({ status: 'authorized', rejectedReason: null });
      break;
    case 'token-entry':
      useAuthStore.setState({ status: 'needsToken', rejectedReason: 'Test fixture: token entry' });
      break;
    case 'server-down':
      useAuthStore.setState({ status: 'serverDown', rejectedReason: null });
      break;
  }

  const queryClient = makeTestQueryClient();

  function Wrapper({ children }: { children: ReactNode }): ReactElement {
    return (
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={[route]}>
          <AuthGate>{children}</AuthGate>
        </MemoryRouter>
      </QueryClientProvider>
    );
  }

  return rtlRender(ui, { wrapper: Wrapper, ...rtlOptions });
}
