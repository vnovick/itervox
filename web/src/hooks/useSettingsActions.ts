import { useMemo } from 'react';
import { useItervoxStore } from '../store/itervoxStore';
import { useToastStore } from '../store/toastStore';
import { authedFetch } from '../auth/authedFetch';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { SettingsError } from '../auth/SettingsError';
import type { AutomationDef } from '../types/schemas';

// Read refreshSnapshot from the store directly (not via selector) so
// the returned action functions have stable references across renders.
function getRefreshSnapshot() {
  return useItervoxStore.getState().refreshSnapshot;
}

function toastError(msg: string) {
  useToastStore.getState().addToast(msg, 'error');
}

// extractServerMessage attempts to parse a structured error body of the form
// `{code, message}` (writeError / writeAutomationValidationError) and returns
// the human-readable message field. Returns undefined for plain-text bodies
// or unrecognised JSON shapes — the caller falls back to errorLabel.
//
// Cloning the response is required because we still want to read the body
// downstream if needed; in this hook we don't, but cloning keeps the
// extractor side-effect-free for future callers.
async function extractServerMessage(res: Response): Promise<string | undefined> {
  try {
    const data = (await res.clone().json()) as unknown;
    if (
      typeof data === 'object' &&
      data !== null &&
      'message' in data &&
      typeof (data as { message: unknown }).message === 'string'
    ) {
      return (data as { message: string }).message;
    }
  } catch {
    // Not JSON — fall through.
  }
  return undefined;
}

// In-flight settings requests indexed by `${method} ${url}`. When a rapid
// second toggle hits the same endpoint while the first is still flying, we
// reuse the in-flight Promise rather than firing a parallel request. This
// turns "click 5 times in 200ms" into a single round-trip and avoids the
// "Network error" toast that AbortError-style failures used to produce when
// the previous request was cancelled by a re-render.
const inFlightSettings = new Map<string, Promise<boolean>>();

// AbortError-shaped throws are surfaced when a fetch is cancelled — by us
// (deliberately) or by the browser when the page navigates. They are not
// user-actionable so we drop them silently rather than toasting.
function isAbortLikeError(err: unknown): boolean {
  if (err instanceof DOMException && err.name === 'AbortError') return true;
  if (typeof err === 'object' && err !== null && 'name' in err) {
    return (err as { name: unknown }).name === 'AbortError';
  }
  return false;
}

async function settingsFetch(
  url: string,
  method: string,
  body?: unknown,
  errorLabel?: string,
): Promise<boolean> {
  const key = `${method} ${url}`;
  const existing = inFlightSettings.get(key);
  if (existing) return existing;

  const run = (async (): Promise<boolean> => {
    try {
      const res = await authedFetch(url, {
        method,
        ...(body !== undefined
          ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
          : {}),
      });
      if (!res.ok) {
        // Prefer the typed SettingsError when the response body parses against
        // ServerErrorSchema. Fall back to extractServerMessage for any other JSON
        // shape (e.g. legacy {message} bodies, plain-text errors).
        const typed = await SettingsError.fromResponse(res);
        const label = errorLabel ?? 'Request failed. Check the server logs.';
        const message = typed?.message ?? (await extractServerMessage(res));
        toastError(message ? `${label.replace(/[.!?]+$/, '')}: ${message}` : label);
        return false;
      }
      await getRefreshSnapshot()();
      return true;
    } catch (err) {
      if (err instanceof UnauthorizedError) return false; // AuthGate handles UI.
      // Browser-aborted requests (rare in practice but surfaced when a
      // component unmounts mid-fetch in StrictMode dev) are not actionable.
      if (isAbortLikeError(err)) return false;
      toastError(errorLabel ? `Network error: ${errorLabel}` : 'Network error.');
      return false;
    } finally {
      inFlightSettings.delete(key);
    }
  })();

  inFlightSettings.set(key, run);
  return run;
}

/**
 * Fetch helper that returns the typed SettingsError on failure (instead of
 * just emitting a toast and returning boolean). For forms that need to pin
 * field-level errors to specific inputs (AutomationFormModal "cron" field
 * validation, etc.).
 *
 * Coexists with settingsFetch — most callers just want toast+rollback and
 * don't need the typed error. New callers wanting field-level validation
 * UX use this variant.
 */
export async function settingsFetchTyped(
  url: string,
  method: string,
  body?: unknown,
): Promise<{ ok: true } | { ok: false; error: SettingsError | null }> {
  try {
    const res = await authedFetch(url, {
      method,
      ...(body !== undefined
        ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
        : {}),
    });
    if (!res.ok) {
      const typed = await SettingsError.fromResponse(res);
      return { ok: false, error: typed };
    }
    await getRefreshSnapshot()();
    return { ok: true };
  } catch (err) {
    if (err instanceof UnauthorizedError) return { ok: false, error: null };
    return { ok: false, error: null };
  }
}

export const __testing = { extractServerMessage };

// Module-level stable action objects — created once, never re-allocated.
const actions = {
  upsertProfile: async (
    name: string,
    command: string,
    backend?: string,
    prompt?: string,
    soul?: string,
    instructions?: string,
    soulFile?: string,
    instructionsFile?: string,
    enabled?: boolean,
    allowedActions?: string[],
    createIssueState?: string,
    originalName?: string,
  ): Promise<boolean> =>
    settingsFetch(
      `/api/v1/settings/profiles/${encodeURIComponent(name)}`,
      'PUT',
      {
        command,
        backend: backend ?? '',
        prompt: prompt ?? '',
        soul: soul ?? '',
        instructions: instructions ?? prompt ?? '',
        soulFile: soulFile ?? '',
        instructionsFile: instructionsFile ?? '',
        enabled: enabled ?? true,
        allowedActions: allowedActions ?? [],
        createIssueState: createIssueState ?? '',
        originalName: originalName ?? '',
      },
      `Failed to save profile "${name}".`,
    ),

  deleteProfile: async (name: string): Promise<boolean> =>
    settingsFetch(
      `/api/v1/settings/profiles/${encodeURIComponent(name)}`,
      'DELETE',
      undefined,
      `Failed to delete profile "${name}".`,
    ),

  updateTrackerStates: async (
    activeStates: string[],
    terminalStates: string[],
    completionState: string,
  ): Promise<boolean> =>
    settingsFetch(
      '/api/v1/settings/tracker/states',
      'PUT',
      { activeStates, terminalStates, completionState },
      'Failed to save tracker states.',
    ),

  setAutoClearWorkspace: async (enabled: boolean): Promise<boolean> =>
    settingsFetch(
      '/api/v1/settings/workspace/auto-clear',
      'POST',
      { enabled },
      'Failed to update auto-clear setting.',
    ),

  setProjectFilter: async (slugs: string[] | null): Promise<boolean> =>
    settingsFetch('/api/v1/projects/filter', 'PUT', { slugs }, 'Failed to update project filter.'),

  addSSHHost: async (host: string, description: string): Promise<boolean> =>
    settingsFetch(
      '/api/v1/settings/ssh-hosts',
      'POST',
      { host, description },
      `Failed to add SSH host "${host}".`,
    ),

  removeSSHHost: async (host: string): Promise<boolean> =>
    settingsFetch(
      `/api/v1/settings/ssh-hosts/${encodeURIComponent(host)}`,
      'DELETE',
      undefined,
      `Failed to remove SSH host "${host}".`,
    ),

  setDispatchStrategy: async (strategy: string): Promise<boolean> =>
    settingsFetch(
      '/api/v1/settings/dispatch-strategy',
      'PUT',
      { strategy },
      'Failed to update dispatch strategy.',
    ),

  setInlineInput: async (enabled: boolean): Promise<boolean> =>
    settingsFetch(
      '/api/v1/settings/inline-input',
      'POST',
      { enabled },
      'Failed to update input handling.',
    ),

  bumpWorkers: async (delta: number): Promise<boolean> =>
    settingsFetch('/api/v1/settings/workers', 'POST', { delta }, 'Failed to update worker count.'),

  setMaxRetries: async (maxRetries: number): Promise<boolean> =>
    settingsFetch(
      '/api/v1/settings/agent/max-retries',
      'PUT',
      { maxRetries },
      'Failed to update max retries.',
    ),

  setFailedState: async (failedState: string): Promise<boolean> =>
    settingsFetch(
      '/api/v1/settings/tracker/failed-state',
      'PUT',
      { failedState },
      'Failed to update failed state.',
    ),

  setMaxSwitchesPerIssuePerWindow: async (maxSwitchesPerIssuePerWindow: number): Promise<boolean> =>
    settingsFetch(
      '/api/v1/settings/agent/max-switches-per-issue-per-window',
      'PUT',
      { maxSwitchesPerIssuePerWindow },
      'Failed to update switch cap.',
    ),

  setSwitchWindowHours: async (switchWindowHours: number): Promise<boolean> =>
    settingsFetch(
      '/api/v1/settings/agent/switch-window-hours',
      'PUT',
      { switchWindowHours },
      'Failed to update switch window.',
    ),

  setReviewerConfig: async (profile: string, autoReview: boolean): Promise<boolean> =>
    settingsFetch(
      '/api/v1/settings/reviewer',
      'PUT',
      { profile, auto_review: autoReview },
      'Failed to update reviewer settings.',
    ),

  setAutomations: async (automations: AutomationDef[]): Promise<boolean> =>
    settingsFetch(
      '/api/v1/settings/automations',
      'PUT',
      { automations },
      'Failed to update automations.',
    ),

  // setAutomationsTyped is the field-level-error-aware variant of
  // setAutomations. Used by AutomationFormModal so the dashboard can pin a
  // server validation error (e.g. "duplicate automation id") to the matching
  // input rather than rendering a generic toast (T-34).
  // The toast layer still fires for non-field-discriminated errors via
  // settingsFetch — callers that don't need field UX keep using setAutomations.
  setAutomationsTyped: async (
    automations: AutomationDef[],
  ): Promise<{ ok: true } | { ok: false; error: SettingsError | null }> =>
    settingsFetchTyped('/api/v1/settings/automations', 'PUT', { automations }),
};

export function useSettingsActions() {
  // Return a stable reference — actions is a module-level singleton.
  // useMemo with [] deps ensures the hook signature matches React conventions.
  return useMemo(() => actions, []);
}
