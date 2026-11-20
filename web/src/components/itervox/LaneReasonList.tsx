import type { LaneReason } from '../../types/schemas';
import { LANE_LABEL, laneReasonKey, laneReasonLocation } from '../../utils/laneReasons';

/** One reason, with its location and code in monospace. */
function ReasonItem({ reason: r }: { reason: LaneReason }) {
  const loc = <code className="font-mono break-all">{laneReasonLocation(r)}</code>;
  if (r.kind === 'finding') {
    return (
      <li className="text-theme-text-secondary">
        {loc}{' '}
        {r.text ? (
          <>
            adds <code className="text-theme-danger-text font-mono break-all">{r.text}</code>{' '}
          </>
        ) : null}
        <span className="text-theme-muted">— {r.message}</span>
      </li>
    );
  }
  if (r.kind === 'lane_rule' && r.rule) {
    return (
      <li className="text-theme-text-secondary">
        {loc} matched <code className="font-mono">{r.rule}</code> →{' '}
        {LANE_LABEL[r.lane ?? ''] ?? r.lane}
      </li>
    );
  }
  return (
    <li className="text-theme-text-secondary">
      {loc}: {r.message}
    </li>
  );
}

/**
 * guardrails.lanes — "why this needs a person": the concrete reasons a
 * held push gives (audit findings with file and line, then the lane
 * pattern each path matched), at most `max`, then how many more.
 */
export function LaneReasonList({
  reasons,
  total,
  max = 8,
}: {
  reasons: readonly LaneReason[];
  total?: number;
  max?: number;
}) {
  if (reasons.length === 0) return null;
  const shown = reasons.slice(0, max);
  const more = Math.max(total ?? 0, reasons.length) - shown.length;
  return (
    <div className="space-y-0.5" data-testid="lane-reasons">
      <p className="text-theme-text text-xs font-semibold">Why this needs a person</p>
      <ul className="list-disc space-y-0.5 pl-4 text-xs" aria-label="Why this needs a person">
        {shown.map((r) => (
          <ReasonItem key={laneReasonKey(r)} reason={r} />
        ))}
      </ul>
      {more > 0 && <p className="text-theme-muted text-xs">and {more} more</p>}
    </div>
  );
}
