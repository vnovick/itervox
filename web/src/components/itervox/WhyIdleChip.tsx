import { ineligibleReasonLabel } from '../../lib/ineligibleReasonLabel';

// CORE-080 — why an idle issue is not dispatching, on cards and list rows.
// Renders nothing without a reason. The visible text is the short label;
// assistive tech hears "Not dispatching: <label>", and the raw machine
// reason is in the tooltip. Static text, not a live region.
export function WhyIdleChip({ reason }: { reason: string | undefined }) {
  if (!reason) return null;
  const { label, title } = ineligibleReasonLabel(reason);
  return (
    <span
      data-testid="why-idle-chip"
      title={title}
      className="bg-theme-warning-soft text-theme-warning-text max-w-[16rem] flex-shrink-0 truncate rounded px-1.5 py-0.5 text-[10px] font-medium"
    >
      <span className="sr-only">Not dispatching: </span>
      {label}
    </span>
  );
}
