// POST /api/v1/settings/models/refresh (admin): re-reads the backends' model
// lists and rewrites agent.available_models in WORKFLOW.md. Shared by the
// Agents page's Models card and the command palette. Toasts the outcome; a
// 401 is left to the AuthGate.

import { ApiError, apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { useToastStore } from '../store/toastStore';

/** Backends the daemon can discover models for (`itervox models refresh`). */
export const REFRESHABLE_BACKENDS = ['claude', 'codex', 'opencode'] as const;

export type ModelsBackend = 'all' | (typeof REFRESHABLE_BACKENDS)[number];

export function isRefreshable(backend: string): backend is ModelsBackend {
  return (REFRESHABLE_BACKENDS as readonly string[]).includes(backend);
}

export async function refreshModels(backend: ModelsBackend): Promise<void> {
  try {
    await apiRequest('/api/v1/settings/models/refresh', {
      op: 'models refresh',
      method: 'POST',
      json: { backend },
    });
    useToastStore
      .getState()
      .addToast(
        `Refreshed ${backend === 'all' ? 'all backends' : backend} — WORKFLOW.md updated`,
        'success',
      );
  } catch (err) {
    if (err instanceof UnauthorizedError) return; // AuthGate handles UI.
    if (err instanceof ApiError && err.status === 501) {
      useToastStore
        .getState()
        .addToast(
          'This daemon build does not implement the refresh endpoint — run `itervox models refresh` from the CLI instead.',
          'info',
        );
      return;
    }
    useToastStore.getState().addToast(`Refresh failed: ${(err as Error).message}`, 'error');
  }
}
