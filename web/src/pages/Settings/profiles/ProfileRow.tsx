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
import { backendBadgeClass, backendLabel } from './profileBadges';

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
  // Until per-profile whitelist/blacklist lands (see planning/skills_pass/),
  // every profile inherits the full inventory surface — we surface that here
  // so operators can see at a glance which agents are over-loaded.
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

  return (
    <article className="border-theme-line bg-theme-bg-soft flex min-h-[176px] w-full flex-col gap-3 rounded-[var(--radius-md)] border p-4">
      <div className="flex w-full items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <p className="text-theme-text text-sm font-semibold">{name}</p>
            <span
              className={`inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium ${backendBadgeClass(inferredBackend)}`}
            >
              {backendLabel(inferredBackend)}
            </span>
            {inferredModel && (
              <span className="bg-theme-panel text-theme-text-secondary rounded-full px-2 py-0.5 font-mono text-[10px]">
                {modelLabel(inferredBackend, inferredModel)}
              </span>
            )}
          </div>
          <p className="text-theme-text-secondary mt-2 text-[11px] leading-relaxed">
            {instructionsText
              ? `${instructionsText.slice(0, 180)}${instructionsText.length > 180 ? '…' : ''}`
              : 'No profile instructions configured yet.'}
          </p>
        </div>
        <span
          className={`rounded-[var(--radius-sm)] px-2.5 py-1 text-[11px] font-medium whitespace-nowrap ${
            isEnabled
              ? 'bg-theme-success-soft text-theme-success'
              : 'bg-theme-panel text-theme-text-secondary'
          }`}
        >
          {isEnabled ? 'Active' : 'Inactive'}
        </span>
      </div>

      {actionLabels.length > 0 && (
        <div className="mt-auto flex flex-wrap gap-1">
          {actionLabels.map((label) => (
            <span
              key={label}
              className="bg-theme-panel text-theme-text-secondary rounded-full px-2 py-0.5 text-[10px]"
            >
              {label}
            </span>
          ))}
        </div>
      )}

      {tokenSummary && (
        <p
          className="text-theme-muted text-[10px]"
          title="Approx. token cost this profile inherits from the global skills inventory. Per-profile whitelist/blacklist coming — see planning/skills_pass."
        >
          Inherits ~{fmtTokens(tokenSummary.total)} tok ({tokenSummary.skillCount} skills) — every
          profile loads the full surface today
        </p>
      )}

      <div className="mt-auto flex flex-wrap items-center gap-2 pt-1">
        <button
          onClick={onEdit}
          className="border-theme-line text-theme-text-secondary rounded-[var(--radius-sm)] border px-3 py-1.5 text-xs transition-colors hover:opacity-80"
        >
          Edit
        </button>
        <button
          onClick={async () => {
            setPendingAction('toggle');
            await onToggleEnabled(name, def, !isEnabled);
            setPendingAction(null);
          }}
          disabled={pendingAction !== null}
          className="bg-theme-accent rounded-[var(--radius-sm)] px-3 py-1.5 text-xs font-medium text-white transition-colors disabled:opacity-50"
        >
          {pendingAction === 'toggle' ? 'Saving…' : isEnabled ? 'Deactivate' : 'Activate'}
        </button>
        {confirmDelete ? (
          <>
            <span className="text-theme-muted text-xs">Delete?</span>
            <button
              onClick={async () => {
                setPendingAction('delete');
                await onDelete(name);
                setPendingAction(null);
                setConfirmDelete(false);
              }}
              disabled={pendingAction !== null}
              className="bg-theme-danger rounded-[var(--radius-sm)] px-2.5 py-1.5 text-xs font-medium text-white transition-colors disabled:opacity-50"
            >
              {pendingAction === 'delete' ? '…' : 'Yes'}
            </button>
            <button
              onClick={() => {
                setConfirmDelete(false);
              }}
              disabled={pendingAction !== null}
              className="border-theme-line text-theme-text-secondary rounded-[var(--radius-sm)] border px-2.5 py-1.5 text-xs transition-colors hover:opacity-80 disabled:opacity-50"
            >
              No
            </button>
          </>
        ) : (
          <button
            onClick={() => {
              setConfirmDelete(true);
            }}
            disabled={pendingAction !== null}
            className="border-theme-danger text-theme-danger rounded-[var(--radius-sm)] border px-3 py-1.5 text-xs transition-colors hover:opacity-80 disabled:opacity-50"
          >
            Delete
          </button>
        )}
      </div>
    </article>
  );
}
