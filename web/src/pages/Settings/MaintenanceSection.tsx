import { ConfirmButton } from '../../components/ui/button/ConfirmButton';
import { useClearAllLogs, useClearAllWorkspaces } from '../../queries/issues';

/**
 * The Settings page's Logs section: clear all logs (operator) and, for
 * admins, reset all workspaces (#214 — DELETE /api/v1/workspaces is admin).
 */
export function MaintenanceSection({ isAdmin }: { isAdmin: boolean }) {
  const clearAllLogs = useClearAllLogs();
  const clearAllWorkspaces = useClearAllWorkspaces();
  return (
    <section aria-labelledby="section-logs">
      <h2 id="section-logs" className="mb-3 text-xs font-semibold tracking-widest uppercase">
        Logs
      </h2>
      <div className="border-theme-line bg-theme-panel space-y-3 rounded-lg border p-4">
        <div className="flex items-center justify-between gap-4">
          <div>
            <p className="text-theme-text text-sm font-medium">Clear all logs</p>
            <p className="text-theme-muted mt-0.5 text-xs">
              Deletes in-memory and on-disk log buffers for all issues.
            </p>
          </div>
          <ConfirmButton
            label="Clear all logs"
            confirmLabel="Yes, clear"
            pendingLabel="Clearing…"
            isPending={clearAllLogs.isPending}
            onConfirm={() => {
              clearAllLogs.mutate(undefined);
            }}
          />
        </div>

        {isAdmin && (
          <>
            <div className="border-theme-line border-t" />

            <div className="flex items-center justify-between gap-4">
              <div>
                <p className="text-theme-text text-sm font-medium">Reset all workspaces</p>
                <p className="text-theme-muted mt-0.5 text-xs">
                  Deletes all cloned workspace directories under workspace.root. Does not affect
                  logs or tracker state.
                </p>
              </div>
              <ConfirmButton
                label="Reset workspaces"
                confirmLabel="Yes, reset"
                pendingLabel="Resetting…"
                isPending={clearAllWorkspaces.isPending}
                onConfirm={() => {
                  clearAllWorkspaces.mutate(undefined);
                }}
              />
            </div>
          </>
        )}
      </div>
    </section>
  );
}
