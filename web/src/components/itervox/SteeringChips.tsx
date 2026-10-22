import { useSteeringPhrases } from '../../queries/sessions';

/**
 * #218 — one-click steering chips (sessions.steering_phrases): each sends
 * its documented message to the running agent through the same steering
 * endpoint as a typed message. Renders nothing when no phrases are
 * configured.
 */
export function SteeringChips({
  onSend,
  disabled = false,
}: {
  onSend: (message: string) => void;
  disabled?: boolean;
}) {
  const phrases = useSteeringPhrases();
  if (phrases.length === 0) return null;
  return (
    <div className="flex flex-wrap gap-1.5" role="group" aria-label="Steering phrases">
      {phrases.map((p) => (
        <button
          key={p.label}
          type="button"
          disabled={disabled}
          title={p.message}
          onClick={() => {
            onSend(p.message);
          }}
          className="border-theme-line text-theme-text-secondary hover:text-theme-text rounded-full border px-2.5 py-0.5 text-[11px] font-medium hover:bg-[var(--bg-soft)] disabled:opacity-50"
        >
          {p.label}
        </button>
      ))}
    </div>
  );
}
