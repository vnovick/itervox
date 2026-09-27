import { useMutation } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { useToastStore } from '../store/toastStore';

interface TestAutomationInput {
  automationId: string;
  identifier: string;
}

/**
 * `useTestAutomation` (T-10) — fires a one-off test dispatch for the named
 * automation against the given issue. The backend tags the resulting run
 * with `triggerType: "test"`, so the timeline / activity surfaces can
 * distinguish test fires from production ones while still showing them
 * under the automation chips.
 */
export function useTestAutomation() {
  return useMutation({
    mutationFn: async ({ automationId, identifier }: TestAutomationInput) => {
      await apiRequest(`/api/v1/automations/${encodeURIComponent(automationId)}/test`, {
        op: 'test fire',
        method: 'POST',
        json: { identifier },
      });
    },
    onError: (err) => {
      const message = err instanceof Error ? err.message : 'Test fire failed.';
      useToastStore.getState().addToast(`Automation test fire failed: ${message}`, 'error');
    },
    onSuccess: () => {
      useToastStore.getState().addToast('Automation test fire dispatched.', 'success');
    },
  });
}
