import { describe, expect, it } from 'vitest';

import { automationDefFromValues, automationValuesFromSuggestion } from '../automationForm';
import { SUGGESTED_AUTOMATIONS } from '../suggestedAutomations';

describe('suggestedAutomations', () => {
  it('includes the v0.2.0 additional automation designs', () => {
    const ids = SUGGESTED_AUTOMATIONS.map((automation) => automation.id);

    expect(ids).toEqual(
      expect.arrayContaining([
        'dependency-readiness',
        'plan-required-gate',
        'planner-pair-claude',
        'planner-pair-codex',
        'debate-moderator',
        'evaluator-optimizer',
        'release-captain',
        'skills-hygiene',
      ]),
    );
  });

  it('converts every suggested automation through the existing form path', () => {
    for (const suggestion of SUGGESTED_AUTOMATIONS) {
      const values = automationValuesFromSuggestion(suggestion);
      const def = automationDefFromValues(values);

      expect(def.id).toBe(suggestion.id);
      expect(def.profile).toBe(suggestion.profile);
      expect(def.trigger.type).toBe(suggestion.triggerType);
    }
  });

  it('configures dependency readiness as blockers-resolved backlog-to-todo automation', () => {
    const readiness = SUGGESTED_AUTOMATIONS.find(
      (automation) => automation.id === 'dependency-readiness',
    );

    expect(readiness).toBeDefined();
    expect(readiness?.profile).toBe('unblock-manager');
    expect(readiness?.triggerType).toBe('blockers_resolved');
    expect(readiness?.states).toEqual(['Backlog']);
    expect(readiness?.moveToState).toBe('Todo');
    expect(readiness?.instructions).toContain('{{ trigger.move_to_state }}');
  });
});
