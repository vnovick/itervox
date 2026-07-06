import type { AutomationDef } from '../../../types/schemas';
import { automationTriggerSummary } from '../../../types/automationTriggers';

function filterSummary(automation: AutomationDef): string {
  const parts: string[] = [];
  if (automation.filter?.matchMode === 'any') parts.push('match any');
  if (automation.filter?.states?.length)
    parts.push(`states: ${automation.filter.states.join(', ')}`);
  if (automation.filter?.labelsAny?.length)
    parts.push(`labels: ${automation.filter.labelsAny.join(', ')}`);
  if (automation.filter?.identifierRegex) parts.push(`regex: ${automation.filter.identifierRegex}`);
  if (automation.filter?.inputContextRegex)
    parts.push(`input: ${automation.filter.inputContextRegex}`);
  if (automation.filter?.limit && automation.filter.limit > 0)
    parts.push(`limit: ${String(automation.filter.limit)}`);
  return parts.length > 0 ? parts.join(' · ') : 'No extra filters';
}

export function AutomationRow({
  automation,
  onEdit,
  onDelete,
}: {
  automation: AutomationDef;
  onEdit: () => void;
  onDelete: () => Promise<void>;
}) {
  return (
    <div
      data-automation-row={automation.id}
      tabIndex={-1}
      className="border-theme-line flex items-start justify-between gap-4 border-t px-5 py-4 first:border-t-0 focus-within:bg-[var(--accent-soft)]/30 focus:bg-[var(--accent-soft)]/30 focus:outline-none"
    >
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-theme-text font-mono text-sm font-semibold">{automation.id}</span>
          <span
            className={`rounded-full px-2 py-0.5 text-[10px] font-medium ${
              automation.enabled
                ? 'bg-[var(--success-soft)] text-[var(--success-strong)]'
                : 'bg-theme-panel text-theme-text-secondary'
            }`}
          >
            {automation.enabled ? 'Enabled' : 'Disabled'}
          </span>
          <span className="rounded-full bg-[var(--accent-soft)] px-2 py-0.5 text-[10px] font-medium text-[var(--accent-strong)]">
            {automation.profile}
          </span>
        </div>
        <p className="text-theme-text-secondary text-xs">
          {automationTriggerSummary(automation.trigger)}
        </p>
        <p className="text-theme-muted text-xs">{filterSummary(automation)}</p>
        {automation.instructions && (
          <p className="text-theme-text-secondary line-clamp-2 text-xs leading-relaxed">
            {automation.instructions}
          </p>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-2">
        <button
          onClick={onEdit}
          className="border-theme-line text-theme-text-secondary rounded-[var(--radius-sm)] border px-3 py-1.5 text-xs transition-colors hover:opacity-80"
        >
          Edit
        </button>
        <button
          onClick={() => {
            void onDelete();
          }}
          className="border-theme-danger text-theme-danger rounded-[var(--radius-sm)] border px-3 py-1.5 text-xs transition-colors hover:opacity-80"
        >
          Delete
        </button>
      </div>
    </div>
  );
}
