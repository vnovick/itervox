import type { DispatchPressure } from '../../../types/schemas';
import { OpsChip } from './OpsChip';

export interface DispatchPressureChipModel {
  label: string;
  // warning is set when the fleet is predominantly DEPENDENCY-bound, which is
  // the counter-intuitive state: slots are sitting idle and adding capacity
  // will not help. Slot-bound pressure is normal under load and stays
  // info-toned.
  warning: boolean;
  title: string;
}

// dominantShareThresholdPercent is the share of observed ticks a constraint
// must reach before it is called out on the chip. Below this, the fleet is
// mostly unconstrained and the extra suffix is noise.
const dominantShareThresholdPercent = 10;

function sharePercent(part: number, total: number): number {
  if (total <= 0) return 0;
  return Math.round((part * 100) / total);
}

// dispatchPressureChipModel derives the chip's label and severity, or null
// when there is nothing meaningful to show yet.
//
// Returns null (chip hidden) when the daemon has not completed a tick, which
// is also the shape older daemons produce — the field is absent from their
// snapshots entirely, so this must tolerate `undefined` rather than assume
// the server always sends it.
export function dispatchPressureChipModel(
  pressure: DispatchPressure | undefined,
): DispatchPressureChipModel | null {
  if (!pressure || pressure.observedTicks <= 0) return null;

  const slotShare = sharePercent(pressure.slotBoundTicks, pressure.observedTicks);
  const depShare = sharePercent(pressure.dependencyBoundTicks, pressure.observedTicks);

  const parts = [`Fleet ${String(pressure.utilizationPercent)}%`];
  // Only the dominant constraint is named. Showing both when they are close
  // would tell the operator nothing actionable, and the strip is already
  // dense.
  const depDominant = depShare >= slotShare && depShare >= dominantShareThresholdPercent;
  const slotDominant = slotShare > depShare && slotShare >= dominantShareThresholdPercent;

  if (depDominant) parts.push(`dep-bound ${String(depShare)}%`);
  else if (slotDominant) parts.push(`slot-bound ${String(slotShare)}%`);

  return {
    label: parts.join(' · '),
    warning: depDominant,
    title: depDominant
      ? `Slots sat idle on ${String(depShare)}% of ticks because remaining work was blocked by dependencies. Raising max_concurrent_agents will not increase throughput — resolve or decompose blockers instead. ${String(pressure.blockedByDependency)} issue(s) blocked on the last tick.`
      : slotDominant
        ? `Every slot was busy with work still waiting on ${String(slotShare)}% of ticks. Raising max_concurrent_agents should increase throughput. ${String(pressure.eligibleWaiting)} issue(s) waiting on capacity on the last tick.`
        : `Mean fleet utilization across ${String(pressure.observedTicks)} ticks. Neither capacity nor dependencies are a dominant constraint.`,
  };
}

// DispatchPressureChip renders the fleet-pressure tile, or nothing at all
// before the first tick. Presentational: it takes the snapshot slice as a
// prop rather than reading the store, so the label logic stays unit-testable
// without mounting a store provider.
export function DispatchPressureChip({ pressure }: { pressure: DispatchPressure | undefined }) {
  const model = dispatchPressureChipModel(pressure);
  if (!model) return null;
  return <OpsChip label={model.label} warning={model.warning} title={model.title} />;
}
