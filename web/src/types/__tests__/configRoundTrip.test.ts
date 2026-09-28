import { describe, expect, it } from 'vitest';
import {
  automationDefToWire,
  unknownAutomationItems,
  withUnknownAllowedActions,
} from '../configRoundTrip';
import { AutomationDefSchema, ProfileDefSchema } from '../schemas';

describe('configRoundTrip', () => {
  it('passes a fully known automation through without unknownFields', () => {
    const def = AutomationDefSchema.parse({
      id: 'a',
      enabled: true,
      profile: 'p',
      trigger: { type: 'cron', cron: '* * * * *' },
    });
    expect(def.unknownFields).toBeUndefined();
    expect(automationDefToWire(def)).toEqual(def);
  });

  it('restores matchMode and a rate_limited switchToBackend', () => {
    const def = AutomationDefSchema.parse({
      id: 'a',
      enabled: true,
      profile: 'p',
      trigger: { type: 'rate_limited' },
      filter: { matchMode: 'most' },
      policy: { switchToProfile: 'q', switchToBackend: 'gemini' },
    });
    expect(def.filter?.matchMode).toBe('all');
    expect(def.policy?.switchToBackend).toBe('');
    const wire = automationDefToWire(def);
    expect(wire).not.toHaveProperty('unknownFields');
    expect(wire.filter?.matchMode).toBe('most');
    expect(wire.policy).toMatchObject({ switchToProfile: 'q', switchToBackend: 'gemini' });
    expect(unknownAutomationItems(def.unknownFields)).toEqual([
      { what: 'match mode', raw: 'most' },
      { what: 'backend', raw: 'gemini' },
    ]);
  });

  it('drops a restored switchToBackend once the trigger is no longer rate_limited', () => {
    const def = AutomationDefSchema.parse({
      id: 'a',
      enabled: true,
      profile: 'p',
      trigger: { type: 'rate_limited' },
      policy: { switchToBackend: 'gemini' },
    });
    const edited = {
      ...def,
      trigger: { type: 'cron' as const, cron: '* * * * *' },
      policy: undefined,
    };
    expect(automationDefToWire(edited).policy).toBeUndefined();
  });

  it('keeps unknown profile actions raw and merges them back once', () => {
    const def = ProfileDefSchema.parse({ command: 'c', allowedActions: ['comment', 'teleport'] });
    expect(def.allowedActions).toEqual(['comment']);
    expect(def.unknownAllowedActions).toEqual(['teleport']);
    expect(withUnknownAllowedActions(['comment'], def.unknownAllowedActions)).toEqual([
      'comment',
      'teleport',
    ]);
    expect(withUnknownAllowedActions(['teleport'], ['teleport'])).toEqual(['teleport']);
    expect(withUnknownAllowedActions(undefined, undefined)).toEqual([]);
  });
});
