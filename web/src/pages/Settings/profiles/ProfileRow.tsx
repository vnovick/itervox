import { unknownValueLabel } from '../../../types/configRoundTrip';
import { useMemo, useState } from 'react';
import type { ProfileDef } from '../../../types/schemas';
import { useSkillsInventory } from '../../../queries/skills';
import {
  agentActionOptionsFor,
  commandToBackend,
  commandToModel,
  modelLabel,
  normalizeAllowedActions,
} from '../profileCommands';
import { backendLabel } from './profileBadges';
import { Button } from '../../../components/ui/button';

function fmtTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 10000) return `${(n / 1000).toFixed(1)}k`;
  return `${String(Math.round(n / 1000))}k`;
}

interface ProfileRowProps {
  name: string;
  def: ProfileDef;
  onEdit: () => void;
  onToggleEnabled: (name: string, def: ProfileDef, enabled: boolean) => Promise<void>;
  onDelete: (name: string) => Promise<void>;
  supportedAgentActions?: readonly string[];
}

export function ProfileRow({
  name,
  def,
  onEdit,
  onToggleEnabled,
  onDelete,
  supportedAgentActions,
}: ProfileRowProps) {
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [pendingAction, setPendingAction] = useState<'toggle' | 'delete' | null>(null);

  const inferredBackend = commandToBackend(def.command, def.backend);
  const inferredModel = commandToModel(def.command);
  const actionLabels = agentActionOptionsFor(supportedAgentActions)
    .filter((option) =>
      normalizeAllowedActions(def.allowedActions, supportedAgentActions).includes(option.id),
    )
    .map((option) => option.label);
  const isEnabled = def.enabled ?? true;
  const instructionsText = (def.instructions ?? def.prompt ?? '').trim();

  // Approx token cost this profile inherits from the global skills inventory.
  // Until per-profile whitelist/blacklist lands, every profile inherits the
  // full inventory surface — we surface the total here so operators can see
  // at a glance which agents are over-loaded.
  const { data: inventory } = useSkillsInventory();
  const tokenSummary = useMemo(() => {
    if (!inventory) return null;
    const skillTokens = (inventory.Skills ?? []).reduce((s, sk) => s + sk.ApproxTokens, 0);
    const pluginTokens = (inventory.Plugins ?? []).reduce((s, p) => s + p.ApproxTokens, 0);
    const hookTokens = (inventory.Hooks ?? []).reduce((s, h) => s + h.ApproxTokens, 0);
    const mcpTokens = (inventory.MCPServers ?? []).length * 800;
    const skillCount = (inventory.Skills ?? []).length;
    return {
      total: skillTokens + pluginTokens + hookTokens + mcpTokens,
      skillCount,
    };
  }, [inventory]);

  const modelText = inferredModel ? modelLabel(inferredBackend, inferredModel) : 'default model';
  const instructionsExcerpt = instructionsText
    ? `${instructionsText.slice(0, 180)}${instructionsText.length > 180 ? '…' : ''}`
    : 'No profile instructions configured yet.';

  return (
    <article className="glass-card flex w-full flex-col gap-1.5 rounded-[18px] px-3.5 py-3">
      <div className="flex w-full flex-wrap items-center gap-x-2.5 gap-y-2">
        <p className="text-theme-text text-[15px] font-semibold">{name}</p>
        <span className={`pill px-2 py-0 text-xs ${isEnabled ? 'pill-ok' : 'pill-neutral'}`}>
          {isEnabled ? 'Active' : 'Inactive'}
        </span>
        <span className="text-theme-text-secondary text-xs">
          {backendLabel(inferredBackend)} ·{' '}
          <span className={inferredModel ? 'font-mono' : ''}>{modelText}</span>
        </span>
        {tokenSummary && (
          <span
            className="text-theme-muted text-xs"
            title="Approx. token cost this profile inherits from the global skills inventory. Every profile loads the full surface today; per-profile whitelist/blacklist is on the roadmap."
          >
            Inherits ~{fmtTokens(tokenSummary.total)} tok ({tokenSummary.skillCount} skills)
          </span>
        )}

        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          <Button
            variant="glass"
            size="md"
            onClick={async () => {
              setPendingAction('toggle');
              await onToggleEnabled(name, def, !isEnabled);
              setPendingAction(null);
            }}
            disabled={pendingAction !== null}
          >
            {pendingAction === 'toggle' ? 'Saving…' : isEnabled ? 'Deactivate' : 'Activate'}
          </Button>
          <Button variant="glass" size="md" onClick={onEdit}>
            Edit
          </Button>
          {confirmDelete ? (
            <>
              <span className="text-theme-muted text-xs">Delete?</span>
              <Button
                variant="destructive"
                size="md"
                onClick={async () => {
                  setPendingAction('delete');
                  await onDelete(name);
                  setPendingAction(null);
                  setConfirmDelete(false);
                }}
                disabled={pendingAction !== null}
              >
                {pendingAction === 'delete' ? '…' : 'Yes'}
              </Button>
              <Button
                variant="glass"
                size="md"
                onClick={() => {
                  setConfirmDelete(false);
                }}
                disabled={pendingAction !== null}
              >
                No
              </Button>
            </>
          ) : (
            <Button
              variant="glass-danger"
              size="md"
              onClick={() => {
                setConfirmDelete(true);
              }}
              disabled={pendingAction !== null}
            >
              Delete
            </Button>
          )}
        </div>
      </div>

      <div className="flex w-full flex-wrap items-center gap-1.5">
        <p className="text-theme-muted min-w-0 flex-1 basis-64 truncate text-xs">
          {instructionsExcerpt}
        </p>
        {actionLabels.map((label) => (
          <span key={label} className="pill pill-neutral px-2 py-0 text-[11px]">
            {label}
          </span>
        ))}
        {/* CORE-047 round 2: actions from a newer daemon, read-only. */}
        {def.unknownAllowedActions?.map((raw) => (
          <span
            key={`unknown:${raw}`}
            title={unknownValueLabel('action', raw)}
            className="pill pill-warn px-2 py-0 text-[11px]"
          >
            {raw} (unknown)
          </span>
        ))}
      </div>
    </article>
  );
}
