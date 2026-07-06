// Test-only QueryClient factory. Disables retries (so a 4xx mock doesn't loop)
// and forces 0ms staleTime + 0ms gcTime so a mutation test sees the new data
// on the next read without race conditions.

import { QueryClient } from '@tanstack/react-query';

export function makeTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 0, gcTime: 0 },
      mutations: { retry: false },
    },
  });
}
