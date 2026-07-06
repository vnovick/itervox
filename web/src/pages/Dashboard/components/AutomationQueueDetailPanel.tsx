import { SlidePanel } from '../../../components/ui/SlidePanel/SlidePanel';
import type {
  AutomationDef,
  AutomationQueueRow,
  DependencyAuditRow,
  ProfileDef,
  RunningRow,
  SSHHostInfo,
} from '../../../types/schemas';
import { automationTriggerSummary, triggerTypeLabel } from '../../../types/automationTriggers';
import { blockerLabel, queuedAge, reasonLabel } from './automationQueueModel';
import { DetailChip, DetailRow, DetailSection, PermissionChip } from './AutomationQueueDetailBits';

// v0.2.0 audit P2-8 — the local `triggerLabel` helper duplicated the
// lookup that `triggerTypeLabel` in `automationTriggers.ts` already
// provides. Use the shared helper as the single source of truth.

function formatBool(value: boolean | undefined): string {
  return value ? 'enabled' : 'disabled';
}

function chipList(values: readonly string[] | undefined, tone: 'neutral' | 'accent' = 'neutral') {
  if (!values || values.length === 0) return null;
  return (
    <div className="flex flex-wrap gap-1.5">
      {values.map((value) => (
        <DetailChip key={value} tone={tone}>
          {value}
        </DetailChip>
      ))}
    </div>
  );
}

function blockerChips(
  blockers: DependencyAuditRow['unresolvedBlockers'],
  tone: 'warning' | 'success',
) {
  if (!blockers || blockers.length === 0) return null;
  return (
    <div className="flex flex-wrap gap-1.5">
      {blockers.map((blocker) => (
        <DetailChip key={blockerLabel(blocker)} tone={tone} title={blocker.state}>
          {blockerLabel(blocker)}
        </DetailChip>
      ))}
    </div>
  );
}

function activityPath(row: AutomationQueueRow, isRunning: boolean) {
  const steps = ['fired', row.status];
  if (isRunning) steps.push('running');
  return (
    <div className="flex flex-wrap gap-1.5">
      {steps.map((step) => (
        <DetailChip key={step} tone={step === 'blocked' ? 'warning' : 'accent'}>
          {step}
        </DetailChip>
      ))}
    </div>
  );
}

export function AutomationQueueDetailPanel({
  row,
  automation,
  dependency,
  profileDef,
  running = [],
  maxConcurrentAgents = 0,
  sshHosts = [],
  onClose,
}: {
  row: AutomationQueueRow | null;
  automation?: AutomationDef;
  dependency?: DependencyAuditRow;
  profileDef?: ProfileDef;
  running?: readonly RunningRow[];
  maxConcurrentAgents?: number;
  sshHosts?: readonly SSHHostInfo[];
  onClose: () => void;
}) {
  if (!row) return null;

  const activeRun = running.find((item) => item.identifier === row.identifier);
  const capacity = `${String(running.length)}/${String(maxConcurrentAgents)}`;
  const workerTarget =
    activeRun?.workerHost ?? (sshHosts.length > 0 ? 'pending SSH allocation' : 'local');
  const triggerSummary = automation
    ? automationTriggerSummary(automation.trigger)
    : triggerTypeLabel(row.triggerType);
  const allowedActions = profileDef?.allowedActions ?? [];

  return (
    <SlidePanel isOpen direction="left" title={`Automation ${row.automationId}`} onClose={onClose}>
      <div
        data-testid="automation-queue-detail-body"
        className="flex-1 space-y-4 overflow-y-auto px-5 py-4"
      >
        <DetailSection title="Summary">
          <DetailRow label="Issue" value={row.identifier} />
          <DetailRow label="Title" value={row.title} />
          <DetailRow label="Status" value={<DetailChip tone="accent">{row.status}</DetailChip>} />
          <DetailRow label="Reason" value={reasonLabel(row)} />
          <DetailRow label="Detail" value={row.reasonDetail} />
          <DetailRow label="Queued" value={queuedAge(row.queuedAt)} />
          <DetailRow
            label="Attempts"
            value={row.attemptCount === 1 ? '1 attempt' : `${String(row.attemptCount)} attempts`}
          />
          <DetailRow label="Activity" value={activityPath(row, Boolean(activeRun))} />
        </DetailSection>

        <DetailSection title="Trigger">
          <DetailRow label="Type" value={triggerTypeLabel(row.triggerType)} />
          <DetailRow label="Configured" value={triggerSummary} />
          <DetailRow label="Cron" value={row.cron ?? automation?.trigger.cron} />
          <DetailRow label="Timezone" value={row.timezone ?? automation?.trigger.timezone} />
          <DetailRow
            label="PR"
            value={
              row.prUrl ? (
                <a href={row.prUrl} className="text-theme-accent hover:underline">
                  {row.prUrl}
                </a>
              ) : null
            }
          />
          <DetailRow label="Input" value={row.inputContext} />
          <DetailRow label="Error" value={row.errorMessage} />
          <DetailRow label="Switch profile" value={row.switchedToProfile} />
          <DetailRow label="Switch backend" value={row.switchedToBackend} />
        </DetailSection>

        <DetailSection title="Dependencies">
          <DetailRow label="Status" value={dependency?.status} />
          <DetailRow label="Sources" value={chipList(dependency?.sources)} />
          <DetailRow
            label="Unresolved"
            value={blockerChips(dependency?.unresolvedBlockers, 'warning')}
          />
          <DetailRow
            label="Resolved"
            value={blockerChips(dependency?.resolvedBlockers, 'success')}
          />
          <DetailRow label="Last audit" value={dependency?.lastAuditedAt} />
          <DetailRow
            label="Transition"
            value={
              dependency?.lastTransitionVersion
                ? `transition #${String(dependency.lastTransitionVersion)}`
                : undefined
            }
          />
          <DetailRow label="Transition reason" value={dependency?.lastTransitionReason} />
        </DetailSection>

        <DetailSection title="Automation Config">
          <DetailRow
            label="State"
            value={
              <DetailChip tone={automation?.enabled ? 'success' : 'neutral'}>
                {formatBool(automation?.enabled)}
              </DetailChip>
            }
          />
          <DetailRow label="Profile" value={automation?.profile ?? row.profile} />
          <DetailRow label="Filter states" value={chipList(automation?.filter?.states)} />
          <DetailRow label="Labels" value={chipList(automation?.filter?.labelsAny)} />
          <DetailRow label="Match mode" value={automation?.filter?.matchMode} />
          <DetailRow label="Limit" value={automation?.filter?.limit} />
          <DetailRow
            label="Policy"
            value={
              automation?.policy?.moveToState
                ? `move to ${automation.policy.moveToState}`
                : undefined
            }
          />
          <DetailRow
            label="Auto resume"
            value={automation?.policy?.autoResume ? 'enabled' : undefined}
          />
        </DetailSection>

        <DetailSection title="Profile">
          <DetailRow label="Command" value={profileDef?.command} />
          <DetailRow label="Backend" value={profileDef?.backend ?? row.backend} />
          <DetailRow label="Create issue state" value={profileDef?.createIssueState} />
          <DetailRow
            label="Permissions"
            value={
              allowedActions.length > 0 ? (
                <div className="flex flex-wrap gap-1.5">
                  {allowedActions.map((action) => (
                    <PermissionChip key={action} action={action} />
                  ))}
                </div>
              ) : undefined
            }
          />
        </DetailSection>

        <DetailSection title="Worker Allocation">
          <DetailRow label="Capacity" value={`capacity ${capacity}`} />
          <DetailRow label="Target" value={workerTarget} />
          <DetailRow label="Active backend" value={activeRun?.backend ?? row.backend} />
          <DetailRow
            label="SSH hosts"
            value={chipList(
              sshHosts.map((host) => host.host),
              'accent',
            )}
          />
        </DetailSection>
      </div>
    </SlidePanel>
  );
}
