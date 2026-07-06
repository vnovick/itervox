import { useState } from 'react';
import { authedFetch } from '../../auth/authedFetch';
import { useToastStore } from '../../store/toastStore';

interface ModelsCardProps {
  availableModels: Record<string, { id: string; label: string }[]>;
}

type Backend = 'all' | 'claude' | 'codex';

/**
 * ModelsCard surfaces the agent.available_models block from WORKFLOW.md and
 * lets the operator refresh it from the Anthropic / OpenAI APIs without
 * leaving the dashboard. Backed by:
 *   GET  /api/v1/settings/models           (read; populated by snapshot)
 *   POST /api/v1/settings/models/refresh   (write; rewrites WORKFLOW.md)
 *
 * Recommended cadence: after a new Claude/Codex release ships, click
 * Refresh once. The CLI equivalent is `itervox models refresh`.
 */
export function ModelsCard({ availableModels }: ModelsCardProps) {
  const [refreshing, setRefreshing] = useState<Backend | null>(null);

  const refresh = async (backend: Backend) => {
    setRefreshing(backend);
    try {
      const res = await authedFetch('/api/v1/settings/models/refresh', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ backend }),
      });
      if (res.status === 501) {
        useToastStore
          .getState()
          .addToast(
            'This daemon build does not implement the refresh endpoint — run `itervox models refresh` from the CLI instead.',
            'info',
          );
        return;
      }
      if (!res.ok) {
        const detail = await res.text();
        useToastStore.getState().addToast(`Refresh failed: ${detail}`, 'error');
        return;
      }
      useToastStore
        .getState()
        .addToast(
          `Refreshed ${backend === 'all' ? 'all backends' : backend} — WORKFLOW.md updated`,
          'success',
        );
    } catch (err) {
      useToastStore.getState().addToast(`Refresh failed: ${(err as Error).message}`, 'error');
    } finally {
      setRefreshing(null);
    }
  };

  const backends = Object.keys(availableModels).sort();

  return (
    <div
      className="bg-theme-bg-card border-theme-line rounded-lg border p-4"
      data-testid="models-card"
    >
      <div className="mb-3 flex items-start justify-between gap-2">
        <div>
          <h3 className="text-theme-text text-sm font-semibold">Available Models</h3>
          <p className="text-theme-muted mt-0.5 text-xs">
            Populated from WORKFLOW.md. Refresh to discover new releases via the Anthropic / OpenAI
            model APIs.
          </p>
        </div>
        <button
          type="button"
          onClick={() => refresh('all')}
          disabled={refreshing !== null}
          className="bg-theme-accent hover:bg-theme-accent/90 disabled:bg-theme-bg-soft text-theme-bg disabled:text-theme-muted rounded px-3 py-1.5 text-xs font-medium disabled:cursor-not-allowed"
          data-testid="models-refresh-all"
        >
          {refreshing === 'all' ? 'Refreshing…' : 'Refresh from APIs'}
        </button>
      </div>

      {backends.length === 0 ? (
        <p className="text-theme-muted text-xs italic">
          No models discovered yet. Click Refresh or run <code>itervox models refresh</code>.
        </p>
      ) : (
        <ul className="space-y-3">
          {backends.map((backend) => {
            const models = availableModels[backend];
            return (
              <li key={backend}>
                <div className="mb-1 flex items-center gap-2">
                  <span className="text-theme-text font-mono text-xs font-semibold uppercase">
                    {backend}
                  </span>
                  <span className="text-theme-muted text-[10px]">{models.length} model(s)</span>
                  <button
                    type="button"
                    onClick={() => refresh(backend as Backend)}
                    disabled={refreshing !== null}
                    className="text-theme-accent disabled:text-theme-muted ml-auto text-[10px] hover:underline"
                    data-testid={`models-refresh-${backend}`}
                  >
                    {refreshing === backend ? 'Refreshing…' : 'Refresh'}
                  </button>
                </div>
                <ul className="text-theme-muted space-y-0.5 text-[11px]">
                  {models.map((m) => (
                    <li key={m.id} className="font-mono">
                      <span className="text-theme-text">{m.id}</span>
                      {m.label && m.label !== m.id ? (
                        <span className="ml-2 italic">{m.label}</span>
                      ) : null}
                    </li>
                  ))}
                </ul>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
