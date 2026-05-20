import { describe, expect, it } from 'vitest';

import { SUGGESTED_PROFILES } from './suggestedProfiles';

describe('suggestedProfiles', () => {
  it('includes the v0.2.0 launch profile library', () => {
    const ids = SUGGESTED_PROFILES.map((profile) => profile.id);

    expect(ids).toEqual(
      expect.arrayContaining([
        'readiness-manager',
        'unblock-manager',
        'planner-claude',
        'planner-codex',
        'debate-moderator',
        'release-captain',
        'failure-analyst',
        'automation-observer',
        'security-boundary-reviewer',
        'capability-curator',
        'docs-maintainer',
        'qa-browser',
      ]),
    );
  });

  it('keeps launch-library profiles comment-only by default', () => {
    const launchIDs = new Set([
      'readiness-manager',
      'unblock-manager',
      'planner-claude',
      'planner-codex',
      'debate-moderator',
      'release-captain',
      'failure-analyst',
      'automation-observer',
      'security-boundary-reviewer',
      'capability-curator',
      'docs-maintainer',
      'qa-browser',
    ]);

    for (const profile of SUGGESTED_PROFILES.filter((item) => launchIDs.has(item.id))) {
      expect(profile.allowedActions).toEqual(['comment']);
    }
  });

  it('uses a backend-compatible Codex planner template', () => {
    const plannerCodex = SUGGESTED_PROFILES.find((profile) => profile.id === 'planner-codex');

    expect(plannerCodex).toBeDefined();
    expect(plannerCodex?.backend).toBe('codex');
    expect(plannerCodex?.model).toBe('gpt-5.3-codex');
  });

  it('gives the built-in input responder comment and provide-input permissions', () => {
    const inputResponder = SUGGESTED_PROFILES.find((profile) => profile.id === 'input-responder');

    expect(inputResponder).toBeDefined();
    expect(inputResponder?.allowedActions).toEqual(['comment', 'provide_input']);
  });

  it('tells the built-in input responder to mirror its answer onto the issue', () => {
    const inputResponder = SUGGESTED_PROFILES.find((profile) => profile.id === 'input-responder');

    expect(inputResponder).toBeDefined();
    expect(inputResponder?.prompt).toContain(
      'Comment the same concise answer on the current issue',
    );
  });
});
