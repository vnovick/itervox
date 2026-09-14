import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { DispatchPressureChip, dispatchPressureChipModel } from '../DispatchPressureChip';
import type { DispatchPressure } from '../../../../types/schemas';

function pressure(overrides: Partial<DispatchPressure> = {}): DispatchPressure {
  return {
    observedTicks: 100,
    slotBoundTicks: 0,
    dependencyBoundTicks: 0,
    utilizationPercent: 50,
    blockedByDependency: 0,
    eligibleWaiting: 0,
    ...overrides,
  };
}

describe('dispatchPressureChipModel', () => {
  // The absent case is not hypothetical: the Go side omits the field until
  // the first tick completes, and older daemons never send it at all.
  it('returns null when the field is absent', () => {
    expect(dispatchPressureChipModel(undefined)).toBeNull();
  });

  it('returns null before the first tick is observed', () => {
    expect(dispatchPressureChipModel(pressure({ observedTicks: 0 }))).toBeNull();
  });

  it('shows utilization alone when neither constraint dominates', () => {
    const model = dispatchPressureChipModel(pressure({ utilizationPercent: 62 }));
    expect(model?.label).toBe('Fleet 62%');
    expect(model?.warning).toBe(false);
  });

  it('names the slot-bound constraint and stays info-toned', () => {
    const model = dispatchPressureChipModel(
      pressure({ slotBoundTicks: 40, utilizationPercent: 95, eligibleWaiting: 3 }),
    );
    expect(model?.label).toBe('Fleet 95% · slot-bound 40%');
    // Slot-bound is the expected state under load, not a problem to flag.
    expect(model?.warning).toBe(false);
    expect(model?.title).toContain('should increase throughput');
  });

  it('warns when dependency-bound dominates', () => {
    const model = dispatchPressureChipModel(
      pressure({ dependencyBoundTicks: 55, utilizationPercent: 20, blockedByDependency: 7 }),
    );
    expect(model?.label).toBe('Fleet 20% · dep-bound 55%');
    // This is the counter-intuitive state worth surfacing: idle slots that
    // more capacity cannot fill.
    expect(model?.warning).toBe(true);
    expect(model?.title).toContain('will not increase throughput');
    expect(model?.title).toContain('7 issue(s) blocked');
  });

  it('suppresses a constraint below the callout threshold', () => {
    // 5% of ticks is noise, not a signal worth spending strip width on.
    const model = dispatchPressureChipModel(
      pressure({ slotBoundTicks: 5, dependencyBoundTicks: 5, utilizationPercent: 30 }),
    );
    expect(model?.label).toBe('Fleet 30%');
  });

  it('names only the dominant constraint when both are above threshold', () => {
    const model = dispatchPressureChipModel(
      pressure({ slotBoundTicks: 15, dependencyBoundTicks: 60, utilizationPercent: 25 }),
    );
    expect(model?.label).toBe('Fleet 25% · dep-bound 60%');
    expect(model?.label).not.toContain('slot-bound');
  });
});

describe('DispatchPressureChip', () => {
  it('renders nothing when there is no pressure data', () => {
    const { container } = render(<DispatchPressureChip pressure={undefined} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('renders the label and the explanatory title', () => {
    render(
      <DispatchPressureChip
        pressure={pressure({ dependencyBoundTicks: 50, utilizationPercent: 18 })}
      />,
    );
    const chip = screen.getByText('Fleet 18% · dep-bound 50%');
    expect(chip).toBeInTheDocument();
    expect(chip).toHaveAttribute('title', expect.stringContaining('blocked by dependencies'));
  });
});
