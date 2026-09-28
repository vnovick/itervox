// CORE-080 — one label table for the machine reasons dispatch.go emits
// (forwarded to TrackerIssue.ineligibleReason by internal/app/enrich.go).
import { describe, expect, it } from 'vitest';
import { ineligibleReasonLabel, INELIGIBLE_REASON_MAX_CHARS } from '../ineligibleReasonLabel';

describe('ineligibleReasonLabel', () => {
  it('maps every reason dispatch.go emits (no_slots, paused_by_state:x, per_state_limit, claimed, already_running, discarding, paused, input_required, pending_input_resume, blocked_by:ENG-2, inferred_blocked_by:src) and truncates unknown strings', () => {
    const cases: [string, string][] = [
      ['no_slots', 'Waiting for a slot'],
      ['paused_by_state:Review', 'Paused while Review is busy'],
      ['per_state_limit', 'State capacity reached'],
      ['claimed', 'Claimed by another agent'],
      ['already_running', 'Running'],
      ['discarding', 'Discarding'],
      ['paused', 'Paused'],
      ['input_required', 'Waiting for input'],
      ['pending_input_resume', 'Waiting for input'],
      ['blocked_by:ENG-2', 'Blocked by ENG-2'],
      ['inferred_blocked_by:src', 'Inferred block: src'],
      // M3 (CORE-053): the dispatch gate held the issue for a limited backend.
      ['backend_limited', 'Agent backend limited'],
      ['missing_fields', 'Missing required fields'],
      ['terminal_state', 'Not in an active state'],
      ['not_active_state', 'Not in an active state'],
    ];
    for (const [reason, label] of cases) {
      expect(ineligibleReasonLabel(reason).label, reason).toBe(label);
      // The raw machine reason stays available for the tooltip.
      expect(ineligibleReasonLabel(reason).title, reason).toContain(reason);
    }

    const long = `some_future_reason:${'x'.repeat(80)}`;
    const unknown = ineligibleReasonLabel(long);
    expect(unknown.label.length).toBeLessThanOrEqual(INELIGIBLE_REASON_MAX_CHARS);
    expect(unknown.label.endsWith('…')).toBe(true);
    expect(unknown.title).toContain(long);
    expect(ineligibleReasonLabel('brand_new').label).toBe('brand_new');
  });

  it('never throws on odd input and keeps prefixed values bounded', () => {
    expect(ineligibleReasonLabel('').label).toBe('Not dispatching');
    expect(ineligibleReasonLabel('blocked_by:').label).toBe('Blocked by another issue');
    expect(ineligibleReasonLabel('inferred_blocked_by:').label).toBe('Inferred block');
    expect(ineligibleReasonLabel('paused_by_state:').label).toBe(
      'Paused while another state is busy',
    );
    const huge = ineligibleReasonLabel(`blocked_by:${'Z'.repeat(100)}`).label;
    expect(huge.length).toBeLessThanOrEqual('Blocked by '.length + INELIGIBLE_REASON_MAX_CHARS);
  });
});
