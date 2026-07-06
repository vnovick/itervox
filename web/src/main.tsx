import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import './index.css';

import App from './App.tsx';
import { ErrorBoundary } from './components/common/ErrorBoundary.tsx';
import { AppWrapper } from './components/common/PageMeta.tsx';
import { ThemeProvider } from './context/ThemeContext.tsx';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ReactQueryDevtools } from '@tanstack/react-query-devtools';
import { AuthGate } from './auth/AuthGate';
import { UnauthorizedError } from './auth/UnauthorizedError';
import { StateSnapshotSchema } from './types/schemas';

declare global {
  interface Window {
    __ITERVOX_VALIDATE_STATE__?: (snapshot: unknown) => { ok: true } | { ok: false; error: string };
  }
}

window.__ITERVOX_VALIDATE_STATE__ = (snapshot) => {
  const parsed = StateSnapshotSchema.safeParse(snapshot);
  return parsed.success ? { ok: true } : { ok: false, error: parsed.error.message };
};

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      // Don't retry on auth failures — the AuthGate will swap the screen.
      // Other errors retry up to 2 times (matches the previous default).
      retry: (count, err) => !(err instanceof UnauthorizedError) && count < 2,
      refetchOnWindowFocus: false,
    },
    // Mutations use the TanStack Query default (no retries), which is
    // already the correct behavior for auth failures.
  },
});

// eslint-disable-next-line @typescript-eslint/no-non-null-assertion
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <ThemeProvider>
          <AppWrapper>
            <AuthGate>
              <App />
            </AuthGate>
          </AppWrapper>
        </ThemeProvider>
        {import.meta.env.DEV && <ReactQueryDevtools initialIsOpen={false} />}
      </QueryClientProvider>
    </ErrorBoundary>
  </StrictMode>,
);
