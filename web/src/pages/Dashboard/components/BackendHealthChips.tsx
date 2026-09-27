import type { AutoSwitchRow, BackendHealthRow } from '../../../types/schemas';
import { OpsChip } from './OpsChip';
import { backendHealthChipModels } from './backendHealthChipModel';
import { ConfirmButton } from '../../../components/ui/button/ConfirmButton';
import { useClearBackendBreaker } from '../../../queries/backends';

// CORE-055 — agent backend health chips for the live-ops strip. One chip per
// backend breaker that is not healthy, labelled by backend (and SSH host) so
// it can never be read as the tracker's own "Tracker rate limited until"
// chip (outbox-derived) or the tracker API budget (rateLimits). Healthy
// backends render nothing ("compact when healthy", like the other
// conditional tiles). limitedUntil is nullable: an unknown reset says so and
// shows the cooldown end as "retry", never as a fake reset time.

export function BackendHealthChips({
  rows,
  autoSwitches,
}: {
  rows: readonly BackendHealthRow[] | undefined;
  autoSwitches: readonly AutoSwitchRow[] | undefined;
}) {
  const chips = backendHealthChipModels(rows);
  const autoCount = autoSwitches?.length ?? 0;
  return (
    <>
      {chips.map((chip) => (
        <span key={chip.key} className="inline-flex items-center gap-1">
          <OpsChip
            label={chip.label}
            title={chip.title}
            danger={chip.danger}
            warning={!chip.danger}
          />
          {/* M3-close V1: operator override when the backend is known to
              be available again. Queued on the daemon's event loop. */}
          {chip.clearable && (
            <ClearBreakerButton name={chip.name} backend={chip.backend} host={chip.host} />
          )}
        </span>
      ))}
      {autoCount > 0 && (
        <OpsChip
          label={`Auto-switched ${String(autoCount)}`}
          title="Issues whose next run uses an automatic backend/profile override"
        />
      )}
    </>
  );
}

// ClearBreakerButton is split out so the mutation hook (and its
// QueryClient) is only needed while some breaker is open.
export function ClearBreakerButton({
  name,
  backend,
  host,
}: {
  name: string;
  backend: string;
  host: string;
}) {
  const clearBreaker = useClearBackendBreaker();
  return (
    <ConfirmButton
      label={`Clear ${name}`}
      confirmLabel="Clear breaker?"
      pendingLabel="Clearing…"
      isPending={clearBreaker.isPending}
      onConfirm={() => {
        clearBreaker.mutate({ backend, host });
      }}
    />
  );
}
