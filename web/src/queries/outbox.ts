/**
 * write-ahead-outbox design, "Surfaces" / Task 4 — per-entry Retry/Discard
 * mutations for the dashboard's Outbox panel.
 *
 * Both endpoints answer 202 Accepted (POST /api/v1/outbox/{id}/retry — 404
 * on an unknown id; DELETE /api/v1/outbox/{id} — idempotent, always 202).
 * Neither call routes through the orchestrator's event loop (the outbox is
 * a standalone, self-contained store on the daemon — see
 * internal/outbox/outbox.go's package doc), but the *observable* effect
 * still only lands on the next snapshot tick, so both mutations call
 * refreshSnapshot() on success rather than trusting the 202 body — mirrors
 * useSetDepsOverride in queries/deps.ts.
 */
import { useMutation } from '@tanstack/react-query';
import { authedFetch } from '../auth/authedFetch';
import { useItervoxStore } from '../store/itervoxStore';
import { useToastStore } from '../store/toastStore';

export function useRetryOutboxEntry() {
  return useMutation<string, Error, string>({
    mutationFn: async (id) => {
      const res = await authedFetch(`/api/v1/outbox/${encodeURIComponent(id)}/retry`, {
        method: 'POST',
      });
      if (!res.ok) {
        const text = await res.text().catch(() => '');
        throw new Error(text || `retry failed (${String(res.status)})`);
      }
      return id;
    },
    onSuccess: () => {
      useToastStore.getState().addToast('Outbox entry queued for immediate retry.', 'success');
      void useItervoxStore.getState().refreshSnapshot();
    },
    onError: (err) => {
      const message = err instanceof Error ? err.message : 'Unknown error';
      useToastStore.getState().addToast(`Outbox retry failed: ${message}`, 'error');
    },
  });
}

export function useDiscardOutboxEntry() {
  return useMutation<string, Error, string>({
    mutationFn: async (id) => {
      const res = await authedFetch(`/api/v1/outbox/${encodeURIComponent(id)}`, {
        method: 'DELETE',
      });
      if (!res.ok) {
        const text = await res.text().catch(() => '');
        throw new Error(text || `discard failed (${String(res.status)})`);
      }
      return id;
    },
    onSuccess: () => {
      useToastStore.getState().addToast('Outbox entry discarded.', 'success');
      void useItervoxStore.getState().refreshSnapshot();
    },
    onError: (err) => {
      const message = err instanceof Error ? err.message : 'Unknown error';
      useToastStore.getState().addToast(`Outbox discard failed: ${message}`, 'error');
    },
  });
}
