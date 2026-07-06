import type { SuggestedProfile } from './suggestedProfiles';
import { backendBadgeClass, backendLabel } from './profileBadges';
import { agentActionOptionsFor } from '../profileCommands';

export function SuggestedProfileCard({
  suggestion,
  onUse,
  supportedAgentActions,
}: {
  suggestion: SuggestedProfile;
  onUse: (s: SuggestedProfile) => void;
  supportedAgentActions?: readonly string[];
}) {
  const actionLabels = agentActionOptionsFor(supportedAgentActions)
    .filter((option) => suggestion.allowedActions.includes(option.id))
    .map((option) => option.label);

  return (
    <button
      type="button"
      onClick={() => {
        onUse(suggestion);
      }}
      className="border-theme-line bg-theme-bg-soft flex min-h-[152px] w-full flex-col items-start gap-3 rounded-[var(--radius-md)] border border-dashed p-4 text-left transition-all hover:opacity-90"
    >
      <div className="flex w-full items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-theme-text text-sm font-semibold">{suggestion.label}</p>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            <span
              className={`inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium ${backendBadgeClass(suggestion.backend)}`}
            >
              {backendLabel(suggestion.backend)}
            </span>
            <span className="bg-theme-panel text-theme-text-secondary rounded-full px-2 py-0.5 font-mono text-[10px]">
              {suggestion.model}
            </span>
          </div>
        </div>
        <span className="bg-theme-accent rounded-[var(--radius-sm)] px-2.5 py-1 text-[11px] font-medium whitespace-nowrap text-white">
          Use Template
        </span>
      </div>
      <p className="text-theme-text-secondary text-[11px] leading-relaxed">
        {suggestion.description}
      </p>
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
    </button>
  );
}
