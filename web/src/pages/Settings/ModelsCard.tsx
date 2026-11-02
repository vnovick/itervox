import { useState } from 'react';
import { ApiError, apiRequest } from '../../auth/apiRequest';
import { UnauthorizedError } from '../../auth/UnauthorizedError';
import { useToastStore } from '../../store/toastStore';
import { Button } from '../../components/ui/button';

interface ModelsCardProps {
  availableModels: Record<string, { id: string; label: string }[]>;
  /** Id for the section heading, so a page can link or label it. */
  headingId?: string;
}

/** Backends the daemon can discover models for (`itervox models refresh`). */
const REFRESHABLE_BACKENDS = ['claude', 'codex', 'opencode'] as const;
type Backend = 'all' | (typeof REFRESHABLE_BACKENDS)[number];

function isRefreshable(backend: string): backend is Backend {
  return (REFRESHABLE_BACKENDS as readonly string[]).includes(backend);
}

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
export function ModelsCard({
  availableModels,
  headingId = 'models-card-heading',
}: ModelsCardProps) {
  const [refreshing, setRefreshing] = useState<Backend | null>(null);

  const refresh = async (backend: Backend) => {
    setRefreshing(backend);
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
    } finally {
      setRefreshing(null);
    }
  };

  const backends = Object.keys(availableModels).sort();

  return (
    <section
      aria-labelledby={headingId}
      className="glass-panel flex flex-col gap-3 rounded-[26px] px-5 py-[18px]"
      data-testid="models-card"
    >
      <div className="flex flex-wrap items-center gap-2.5">
        <h2 id={headingId} className="text-theme-text mr-auto text-base font-semibold">
          Available Models
        </h2>
        <Button
          variant="glass"
          size="lg"
          pill
          onClick={() => refresh('all')}
          disabled={refreshing !== null}
          data-testid="models-refresh-all"
        >
          {refreshing === 'all' ? 'Refreshing…' : 'Refresh from APIs'}
        </Button>
      </div>
      <p className="text-theme-text-secondary text-[13px]">
        Populated from WORKFLOW.md. Refresh to discover new releases via the Anthropic / OpenAI
        model APIs and, when installed, <code>opencode models</code>.
      </p>

      {backends.length === 0 ? (
        <p className="glass-card text-theme-muted px-3.5 py-3 text-[13px] italic">
          No models discovered yet. Click Refresh or run <code>itervox models refresh</code>.
        </p>
      ) : (
        <ul className="flex flex-col gap-2">
          {backends.map((backend) => {
            const models = availableModels[backend];
            return (
              <li key={backend} className="glass-card px-3.5 py-2.5 text-[13px]">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-theme-text font-mono font-semibold">{backend}</span>
                  <span className="text-theme-muted text-xs">{models.length} model(s)</span>
                  {isRefreshable(backend) && (
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => refresh(backend)}
                      disabled={refreshing !== null}
                      className="text-theme-accent-text ml-auto"
                      data-testid={`models-refresh-${backend}`}
                    >
                      {refreshing === backend ? 'Refreshing…' : 'Refresh'}
                    </Button>
                  )}
                </div>
                <ul className="text-theme-text-secondary mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs">
                  {models.map((m) => (
                    <li key={m.id} className="font-mono">
                      <span className="text-theme-text">{m.id}</span>
                      {m.label && m.label !== m.id ? (
                        <span className="text-theme-muted ml-1.5 font-sans">{m.label}</span>
                      ) : null}
                    </li>
                  ))}
                </ul>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
