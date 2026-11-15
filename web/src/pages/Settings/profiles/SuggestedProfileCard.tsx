import type { SuggestedProfile } from './suggestedProfiles';
import { backendLabel } from './profileBadges';
import { agentActionOptionsFor } from '../profileCommands';
import { TemplateTile, TileMeta } from '../../../components/itervox/TemplateTile';

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
    <TemplateTile
      title={suggestion.label}
      tooltip={actionLabels.length > 0 ? `Actions: ${actionLabels.join(', ')}` : undefined}
      description={suggestion.description}
      onUse={() => {
        onUse(suggestion);
      }}
      meta={
        <>
          <TileMeta>{backendLabel(suggestion.backend)}</TileMeta>
          <TileMeta mono>{suggestion.model}</TileMeta>
        </>
      }
    />
  );
}
