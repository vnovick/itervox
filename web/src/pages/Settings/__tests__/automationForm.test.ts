import { describe, expect, it } from 'vitest';
import {
  automationDefFromValues,
  automationFormSchema,
  automationValuesFromDef,
  automationValuesFromSuggestion,
  emptyAutomationValues,
  nextAutomationId,
  type AutomationFormValues,
} from '../automations/automationForm';

const baseValues: AutomationFormValues = {
  id: 'qa-ready',
  enabled: true,
  profile: 'qa',
  instructions: ' Run QA. ',
  triggerType: 'cron',
  triggerState: '',
  cron: '0 9 * * 1-5',
  timezone: 'UTC',
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
  moveToState: '',
};

describe('automationFormSchema', () => {
  it('rejects invalid identifier regexes', () => {
    const result = automationFormSchema.safeParse({
      id: 'comment-watch',
      enabled: true,
      profile: 'pm',
      instructions: '',
      triggerType: 'tracker_comment_added',
      triggerState: '',
      cron: '',
      timezone: '',
      matchMode: 'all',
      states: [],
      labelsAny: [],
      identifierRegex: '[',
      limit: '',
      inputContextRegex: '',
      maxAgeMinutes: '',
      autoResume: false,
      switchToProfile: '',
      switchToBackend: '',
      cooldownMinutes: '',
      moveToState: '',
    });

    expect(result.success).toBe(false);
    if (result.success) {
      throw new Error('expected invalid identifier regex to fail');
    }
    expect(result.error.issues.some((issue) => issue.path[0] === 'identifierRegex')).toBe(true);
  });

  it('rejects invalid input-context regexes', () => {
    const result = automationFormSchema.safeParse({
      id: 'input-responder',
      enabled: true,
      profile: 'pm',
      instructions: '',
      triggerType: 'input_required',
      triggerState: '',
      cron: '',
      timezone: '',
      matchMode: 'all',
      states: [],
      labelsAny: [],
      identifierRegex: '',
      limit: '',
      inputContextRegex: '[',
      maxAgeMinutes: '',
      autoResume: false,
      switchToProfile: '',
      switchToBackend: '',
      cooldownMinutes: '',
      moveToState: '',
    });

    expect(result.success).toBe(false);
    if (result.success) {
      throw new Error('expected invalid input context regex to fail');
    }
    expect(result.error.issues.some((issue) => issue.path[0] === 'inputContextRegex')).toBe(true);
  });

  // Gap A — maxAgeMinutes accepts only non-negative integer strings.
  it('rejects negative or non-integer maxAgeMinutes values', () => {
    const base = {
      id: 'input-responder',
      enabled: true,
      profile: 'pm',
      instructions: '',
      triggerType: 'input_required' as const,
      triggerState: '',
      cron: '',
      timezone: '',
      matchMode: 'all' as const,
      states: [],
      labelsAny: [],
      identifierRegex: '',
      limit: '',
      inputContextRegex: '',
      autoResume: false,
      switchToProfile: '',
      switchToBackend: '' as const,
      cooldownMinutes: '',
      moveToState: '',
    };
    for (const bad of ['-1', '1.5', 'soon']) {
      const result = automationFormSchema.safeParse({ ...base, maxAgeMinutes: bad });
      expect(result.success, `${bad} should fail`).toBe(false);
    }
    for (const good of ['', '0', '60', '1440']) {
      const result = automationFormSchema.safeParse({ ...base, maxAgeMinutes: good });
      expect(result.success, `${good} should pass`).toBe(true);
    }
  });

  it('serializes populated cron filters and trims optional fields', () => {
    const def = automationDefFromValues({
      ...baseValues,
      id: ' qa-ready ',
      matchMode: 'any',
      states: ['Ready for QA'],
      labelsAny: ['qa'],
      identifierRegex: ' ENG-\\\\d+ ',
      limit: '10',
      instructions: ' Run QA. ',
    });

    expect(def).toMatchObject({
      id: 'qa-ready',
      instructions: 'Run QA.',
      trigger: { type: 'cron', cron: '0 9 * * 1-5', timezone: 'UTC' },
      filter: {
        matchMode: 'any',
        states: ['Ready for QA'],
        labelsAny: ['qa'],
        identifierRegex: 'ENG-\\\\d+',
        limit: 10,
      },
    });
  });

  it('serializes input-required max age only for input-required triggers', () => {
    const inputDef = automationDefFromValues({
      ...baseValues,
      triggerType: 'input_required',
      cron: '',
      timezone: '',
      inputContextRegex: 'continue|branch',
      maxAgeMinutes: '60',
      autoResume: true,
    });
    expect(inputDef.filter?.maxAgeMinutes).toBe(60);
    expect(inputDef.policy?.autoResume).toBe(true);

    const commentDef = automationDefFromValues({
      ...baseValues,
      triggerType: 'tracker_comment_added',
      cron: '',
      timezone: '',
      maxAgeMinutes: '60',
    });
    expect(commentDef.filter?.maxAgeMinutes).toBeUndefined();
  });

  it('serializes rate-limited switching policy only on rate-limited triggers', () => {
    const rateLimited = automationDefFromValues({
      ...baseValues,
      triggerType: 'rate_limited',
      cron: '',
      timezone: '',
      autoResume: true,
      switchToProfile: ' fallback ',
      switchToBackend: 'codex',
      cooldownMinutes: '45',
    });
    expect(rateLimited.policy).toEqual({
      autoResume: true,
      switchToProfile: 'fallback',
      switchToBackend: 'codex',
      cooldownMinutes: 45,
    });

    const runFailed = automationDefFromValues({
      ...baseValues,
      triggerType: 'run_failed',
      cron: '',
      timezone: '',
      switchToProfile: 'fallback',
      switchToBackend: 'codex',
      cooldownMinutes: '45',
    });
    expect(runFailed.policy).toBeUndefined();
  });

  it('serializes blockers-resolved move-to-state policy only on blockers-resolved triggers', () => {
    const blockersResolved = automationDefFromValues({
      ...baseValues,
      triggerType: 'blockers_resolved',
      cron: '',
      timezone: '',
      moveToState: ' Todo ',
    });
    expect(blockersResolved.policy).toEqual({ moveToState: 'Todo' });

    const cron = automationDefFromValues({
      ...baseValues,
      moveToState: 'Todo',
    });
    expect(cron.policy).toBeUndefined();
  });

  it('loads move-to-state policy from an existing blockers-resolved automation', () => {
    const values = automationValuesFromDef({
      id: 'unblock-backlog-to-todo',
      enabled: true,
      profile: 'pm',
      instructions: '',
      trigger: { type: 'blockers_resolved' },
      policy: { moveToState: 'Todo' },
    });

    expect(values.moveToState).toBe('Todo');
  });

  it('requires auto-switch for rate-limited switching rules', () => {
    const result = automationFormSchema.safeParse({
      ...baseValues,
      triggerType: 'rate_limited',
      cron: '',
      timezone: '',
      switchToProfile: 'fallback',
      autoResume: false,
    });
    expect(result.success).toBe(false);
    if (result.success) {
      throw new Error('expected rate_limited without auto-switch to fail');
    }
    expect(result.error.issues.some((issue) => issue.path[0] === 'autoResume')).toBe(true);
  });

  it('round-trips existing definitions and suggestion defaults', () => {
    expect(nextAutomationId([{ id: 'automation-2' }, { id: 'automation-3' }] as never)).toBe(
      'automation-4',
    );
    expect(emptyAutomationValues('default', [{ id: 'automation-2' }] as never)).toMatchObject({
      id: 'automation-3',
      profile: 'default',
      cron: '0 9 * * 1-5',
    });

    expect(
      automationValuesFromDef({
        id: 'rate-limit',
        enabled: true,
        profile: 'fallback',
        instructions: 'Switch.',
        trigger: { type: 'rate_limited' },
        filter: { limit: 0, maxAgeMinutes: 0 },
        policy: { autoResume: true, switchToProfile: 'fallback', cooldownMinutes: 30 },
      }),
    ).toMatchObject({
      triggerType: 'rate_limited',
      limit: '',
      maxAgeMinutes: '',
      autoResume: true,
      switchToProfile: 'fallback',
      cooldownMinutes: '30',
    });

    expect(
      automationValuesFromSuggestion({
        id: 'comment-watch',
        profile: 'pm',
        label: 'Comment watch',
        description: 'Watch comments.',
        instructions: 'Summarize.',
        triggerType: 'tracker_comment_added',
        labelsAny: ['triage'],
        limit: 5,
        autoResume: true,
      }),
    ).toMatchObject({
      id: 'comment-watch',
      labelsAny: ['triage'],
      limit: '5',
      autoResume: true,
    });
  });
});
