import type { SuggestedAutomation } from './suggestedAutomations';
import { AUTOMATION_TRIGGER_META } from '../../../types/automationTriggers';

export function SuggestedAutomationCard({
  suggestion,
  disabled = false,
  onUse,
}: {
  suggestion: SuggestedAutomation;
  disabled?: boolean;
  onUse: (suggestion: SuggestedAutomation) => void;
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={() => {
        onUse(suggestion);
      }}
      className="border-theme-line bg-theme-bg-soft flex min-h-[160px] w-full flex-col items-start gap-3 rounded-[var(--radius-md)] border border-dashed p-4 text-left transition-all hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-55"
    >
      <div className="flex w-full items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-theme-text text-sm font-semibold">{suggestion.label}</p>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            <span className="bg-theme-panel text-theme-text-secondary rounded-full px-2 py-0.5 text-[10px] font-medium">
              {AUTOMATION_TRIGGER_META[suggestion.triggerType].label}
            </span>
            <span className="rounded-full bg-[var(--accent-soft)] px-2 py-0.5 text-[10px] font-medium text-[var(--accent-strong)]">
              {suggestion.profile}
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
      {disabled && (
        <p className="text-theme-danger text-[11px] leading-relaxed">
          Create and enable the <span className="font-medium">{suggestion.profile}</span> profile
          first.
        </p>
      )}
    </button>
  );
}
