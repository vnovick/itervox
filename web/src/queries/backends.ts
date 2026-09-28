/**
 * M3-close V1 — operator clear of an agent-backend circuit breaker
 * (POST /api/v1/backend-health/clear). The daemon queues the clear on its
 * event loop and answers 202, so the effect lands on the next snapshot:
 * refreshSnapshot() on success, never patchSnapshot.
 */
import { useMutation } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { useItervoxStore } from '../store/itervoxStore';
import { useToastStore } from '../store/toastStore';

export interface ClearBackendBreakerVars {
  backend: string;
  host?: string;
}

export function useClearBackendBreaker() {
  return useMutation<ClearBackendBreakerVars, Error, ClearBackendBreakerVars>({
    mutationFn: async ({ backend, host }) => {
      await apiRequest('/api/v1/backend-health/clear', {
        op: 'clearBackendBreaker',
        method: 'POST',
        json: { backend, host: host ?? '' },
      });
      return { backend, host };
    },
    onSuccess: () => {
      void useItervoxStore.getState().refreshSnapshot();
    },
    onError: (err, { backend }) => {
      useToastStore
        .getState()
        .addToast(`Clearing the ${backend} breaker failed: ${err.message}`, 'error');
    },
  });
}
