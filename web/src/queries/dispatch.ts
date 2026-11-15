/**
 * #213 — pause and resume ALL dispatch (POST /api/v1/dispatch/pause and
 * /api/v1/dispatch/resume). The daemon applies the change on its event loop
 * and answers with the pause it applied; the dashboard then reads the
 * authoritative state with refreshSnapshot(), never patchSnapshot.
 */
import { useMutation } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { useItervoxStore } from '../store/itervoxStore';
import { useToastStore } from '../store/toastStore';

export interface SetDispatchPauseVars {
  /** true pauses all dispatch, false resumes it. */
  paused: boolean;
  /** Optional operator reason, shown on the dashboard and in the feed. */
  reason?: string;
}

export function useSetDispatchPause() {
  return useMutation<SetDispatchPauseVars, Error, SetDispatchPauseVars>({
    mutationFn: async (vars) => {
      if (vars.paused) {
        const reason = vars.reason?.trim();
        await apiRequest('/api/v1/dispatch/pause', {
          op: 'pauseDispatch',
          method: 'POST',
          json: reason ? { reason } : {},
        });
      } else {
        await apiRequest('/api/v1/dispatch/resume', { op: 'resumeDispatch', method: 'POST' });
      }
      return vars;
    },
    onSuccess: () => {
      void useItervoxStore.getState().refreshSnapshot();
    },
    onError: (err, { paused }) => {
      if (err instanceof UnauthorizedError) return; // AuthGate handles the UI
      useToastStore
        .getState()
        .addToast(`${paused ? 'Pausing' : 'Resuming'} dispatch failed: ${err.message}`, 'error');
    },
  });
}
