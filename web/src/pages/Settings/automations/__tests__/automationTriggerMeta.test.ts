import { describe, expect, it } from 'vitest';
import {
  AUTOMATION_TRIGGER_META,
  AUTOMATION_TRIGGER_OPTIONS,
  AUTOMATION_TRIGGER_TYPES,
  automationTriggerSummary,
} from '../../../../types/automationTriggers';
import { automationFormSchema } from '../automationForm';

const baseValues = {
  id: 'meta-test',
  enabled: true,
  profile: 'default',
  instructions: '',
  triggerState: '',
  cron: '',
  timezone: '',
  matchMode: 'all',
  states: [],
  labelsAny: [],
  identifierRegex: '',
  limit: '',
  inputContextRegex: '',
  maxAgeMinutes: '',
  autoResume: false,
  switchToProfile: '',
  switchToBackend: '',
  cooldownMinutes: '',
} as const;

describe('automation trigger metadata', () => {
  it('has one metadata entry and option for every trigger type', () => {
    expect(Object.keys(AUTOMATION_TRIGGER_META)).toEqual([...AUTOMATION_TRIGGER_TYPES]);
    expect(AUTOMATION_TRIGGER_OPTIONS.map((option) => option.value)).toEqual([
      ...AUTOMATION_TRIGGER_TYPES,
    ]);
  });

  it('keeps the form schema exhaustive with the shared trigger list', () => {
    for (const triggerType of AUTOMATION_TRIGGER_TYPES) {
      const values = {
        ...baseValues,
        triggerType,
        cron: triggerType === 'cron' ? '0 9 * * 1-5' : '',
        triggerState: triggerType === 'issue_entered_state' ? 'Todo' : '',
        autoResume: triggerType === 'rate_limited',
        switchToProfile: triggerType === 'rate_limited' ? 'codex-fallback' : '',
      };

      expect(automationFormSchema.safeParse(values).success, triggerType).toBe(true);
    }
  });

  it('summarizes non-cron triggers without falling through to missing cron text', () => {
    expect(automationTriggerSummary({ type: 'pr_opened' })).toBe('PR Opened');
    expect(automationTriggerSummary({ type: 'rate_limited' })).toBe('Rate Limited');
  });
});
