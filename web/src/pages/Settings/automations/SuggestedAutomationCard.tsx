import type { SuggestedAutomation } from './suggestedAutomations';
import { AUTOMATION_TRIGGER_META } from '../../../types/automationTriggers';
import { TemplateTile, TileMeta } from '../../../components/itervox/TemplateTile';

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
    <TemplateTile
      title={suggestion.label}
      description={suggestion.description}
      disabled={disabled}
      onUse={() => {
        onUse(suggestion);
      }}
      meta={
        <>
          <TileMeta>{AUTOMATION_TRIGGER_META[suggestion.triggerType].label}</TileMeta>
          <TileMeta>{suggestion.profile}</TileMeta>
        </>
      }
      note={
        disabled ? (
          <>
            Create and enable the <span className="font-medium">{suggestion.profile}</span> profile
            first.
          </>
        ) : undefined
      }
    />
  );
}
