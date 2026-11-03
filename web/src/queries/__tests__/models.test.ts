import { describe, expect, it } from 'vitest';
import { isRefreshable, REFRESHABLE_BACKENDS } from '../models';

describe('isRefreshable', () => {
  it('offers a refresh for every backend the daemon can list models for', () => {
    expect(REFRESHABLE_BACKENDS).toEqual(['claude', 'codex', 'opencode']);
    for (const backend of REFRESHABLE_BACKENDS) expect(isRefreshable(backend)).toBe(true);
  });

  it('offers none for backends without model discovery', () => {
    expect(isRefreshable('cursor')).toBe(false);
    expect(isRefreshable('custom')).toBe(false);
  });
});
