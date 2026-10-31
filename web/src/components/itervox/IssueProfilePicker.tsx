import { useSetIssueProfile } from '../../queries/issues';
import { useProfileBackends } from '../../queries/promptRefs';
import { useSkillsInventory } from '../../queries/skills';
import { useItervoxStore } from '../../store/itervoxStore';
import type { TrackerIssue } from '../../types/schemas';
import { EMPTY_PROFILE_LABEL, EMPTY_PROFILES } from '../../utils/format';
import { normalizeBackend, type SupportedBackend } from '../../pages/Settings/profileCommands';
import {
  backendItems,
  describeCounts,
  guessProfileBackend,
  itemCounts,
} from '../../pages/Settings/profiles/backendInventory';
import { backendLabel } from '../../pages/Settings/profiles/profileBadges';

/** How many item names the summary spells out before "and N more". */
const SUMMARY_NAMES = 6;

// The issue's agent-profile picker (extracted from IssueDetailSlide, #123).
// Each option names the backend the profile runs on — resolved by the
// daemon the way the runner resolves it — and the line under the picker
// lists the skills, subagents and prompts that backend can use for this
// issue (a per-issue backend pin, CORE-056, wins over the profile's).
export function IssueProfilePicker({
  issue,
  locked,
}: {
  issue: TrackerIssue;
  /** Locked while running or in the working state (CORE-070). */
  locked: boolean;
}) {
  const availableProfiles = useItervoxStore((s) => s.snapshot?.availableProfiles ?? EMPTY_PROFILES);
  const profileDefs = useItervoxStore((s) => s.snapshot?.profileDefs);
  const snapshotDefault = useItervoxStore((s) => s.snapshot?.defaultBackend);
  const setIssueProfile = useSetIssueProfile();
  const { data: backends } = useProfileBackends();
  const { data: inventory } = useSkillsInventory();

  const defaultBackend = normalizeBackend(backends?.default ?? snapshotDefault);
  const backendOf = (profile: string): SupportedBackend => {
    if (!profile) return defaultBackend;
    const resolved = backends?.profiles[profile];
    if (resolved) return normalizeBackend(resolved);
    const def = profileDefs?.[profile];
    return guessProfileBackend(def?.command, def?.backend, defaultBackend);
  };

  const current = issue.agentProfile ?? '';
  const effective: SupportedBackend = issue.agentBackend
    ? normalizeBackend(issue.agentBackend)
    : backendOf(current);
  const items = backendItems(inventory, effective);

  if (availableProfiles.length === 0) return null;

  const names = items.map((item) => (item.kind === 'prompt' ? `prompts:${item.name}` : item.name));
  const shown = names.slice(0, SUMMARY_NAMES).join(', ');
  const more =
    names.length > SUMMARY_NAMES ? `, and ${String(names.length - SUMMARY_NAMES)} more` : '';

  return (
    <div>
      <h4 className="field-label mb-1.5">Agent Profile</h4>
      {locked ? (
        <div className="flex items-center gap-2">
          <span className="text-theme-text-secondary text-xs">
            {issue.agentProfile ?? EMPTY_PROFILE_LABEL}
          </span>
          <span className="tag pill-warn">
            locked while {issue.orchestratorState === 'running' ? 'running' : issue.state}
          </span>
        </div>
      ) : (
        <select
          aria-label="Agent profile"
          aria-describedby={
            inventory ? `issue-profile-capabilities-${issue.identifier}` : undefined
          }
          value={current}
          onChange={(e) => {
            setIssueProfile.mutate({ identifier: issue.identifier, profile: e.target.value });
          }}
          className="glass-chip text-theme-text focus-visible:ring-theme-accent min-h-10 min-w-[160px] cursor-pointer rounded-[var(--radius-sm)] px-3 text-[13px] focus:outline-none focus-visible:ring-2"
        >
          <option value="">
            {EMPTY_PROFILE_LABEL} · {backendLabel(defaultBackend)}
          </option>
          {availableProfiles.map((p) => (
            <option key={p} value={p}>
              {p} · {backendLabel(backendOf(p))}
            </option>
          ))}
        </select>
      )}
      {inventory && (
        <p
          id={`issue-profile-capabilities-${issue.identifier}`}
          data-testid="issue-profile-capabilities"
          className="text-theme-muted mt-1 text-[11px]"
          title={names.join(', ')}
        >
          {backendLabel(effective)} can use {describeCounts(itemCounts(items))}
          {shown ? `: ${shown}${more}` : ''}.
        </p>
      )}
    </div>
  );
}
