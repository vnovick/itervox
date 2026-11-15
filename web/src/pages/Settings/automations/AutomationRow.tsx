import { Button } from '../../../components/ui/button';
import type { AutomationDef } from '../../../types/schemas';
import { automationTriggerSummary } from '../../../types/automationTriggers';
import { unknownValueLabel } from '../../../types/configRoundTrip';
import { filterLabel } from '../../../utils/filterGroups';

function filterSummary(automation: AutomationDef): string {
  const parts: string[] = [];
  const rawMatchMode = automation.unknownFields?.matchMode;
  if (rawMatchMode !== undefined) parts.push(unknownValueLabel('match mode', rawMatchMode));
  else if (automation.filter?.matchMode === 'any') parts.push('match any');
  if (automation.filter?.states?.length)
    parts.push(`states: ${automation.filter.states.join(', ')}`);
  if (automation.filter?.labelsAny?.length)
    parts.push(`labels: ${automation.filter.labelsAny.join(', ')}`);
  if (automation.filter?.groups?.length)
    parts.push(`groups: ${filterLabel(automation.filter.groups)}`);
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
  const triggerText = automation.unknownFields?.trigger
    ? unknownValueLabel('trigger', automation.unknownFields.trigger.type)
    : automationTriggerSummary(automation.trigger);
  return (
    <div
      data-automation-row={automation.id}
      tabIndex={-1}
      className="glass-card focus:ring-theme-accent flex w-full flex-col gap-1.5 rounded-[18px] px-3.5 py-3 focus:ring-2 focus:outline-none"
    >
      <div className="flex w-full flex-wrap items-center gap-x-2.5 gap-y-2">
        <span className="text-theme-text font-mono text-sm font-semibold">{automation.id}</span>
        <span
          className={`pill px-2 py-0 text-xs ${automation.enabled ? 'pill-ok' : 'pill-neutral'}`}
        >
          {automation.enabled ? 'Enabled' : 'Disabled'}
        </span>
        <span className="pill pill-accent px-2 py-0 text-xs">{triggerText}</span>
        <span className="text-theme-muted text-xs">
          profile <span className="text-theme-text-secondary">{automation.profile}</span>
        </span>
        {automation.instructions && (
          <span className="text-theme-muted min-w-0 flex-1 basis-48 truncate text-xs">
            {automation.instructions}
          </span>
        )}
        <div className="ml-auto flex shrink-0 items-center gap-1.5">
          <Button variant="glass" size="md" onClick={onEdit}>
            Edit
          </Button>
          <Button
            variant="glass-danger"
            size="md"
            onClick={() => {
              void onDelete();
            }}
          >
            Delete
          </Button>
        </div>
      </div>
      <p className="text-theme-muted text-xs">{filterSummary(automation)}</p>
    </div>
  );
}
