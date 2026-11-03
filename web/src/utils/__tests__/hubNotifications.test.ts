import { describe, expect, it } from 'vitest';
import {
  effectiveRoute,
  matrixProjects,
  setSnooze,
  snoozedUntil,
  toggleRoute,
} from '../hubNotifications';
import type { HubNotificationSettings } from '../../types/schemas';

const BASE: HubNotificationSettings = {
  version: 1,
  digest_interval_minutes: 15,
  channels: {},
  defaults: { run_failed: ['push', 'desktop'], pr_merged: [] },
  projects: { beta: { routes: { run_failed: [] } } },
};

// #229 — the settings edits behind the Notifications matrix.
describe('hubNotifications', () => {
  it('reads a project route, else the default', () => {
    expect(effectiveRoute(BASE, 'alpha', 'run_failed')).toEqual(['push', 'desktop']);
    expect(effectiveRoute(BASE, 'beta', 'run_failed')).toEqual([]);
    expect(effectiveRoute(BASE, 'alpha', 'unknown')).toEqual([]);
  });

  it('toggles a channel into a project route without touching the defaults', () => {
    const on = toggleRoute(BASE, 'alpha', 'pr_merged', 'slack', true);
    expect(on.projects.alpha.routes?.pr_merged).toEqual(['slack']);
    expect(
      toggleRoute(on, 'alpha', 'pr_merged', 'slack', true).projects.alpha.routes?.pr_merged,
    ).toEqual(['slack']);
    const off = toggleRoute(BASE, 'alpha', 'run_failed', 'push', false);
    expect(off.projects.alpha.routes?.run_failed).toEqual(['desktop']);
    expect(BASE.defaults.run_failed).toEqual(['push', 'desktop']);
  });

  it('sets and clears a snooze', () => {
    const now = Date.parse('2026-10-22T09:00:00Z');
    const s = setSnooze(BASE, 'alpha', '2026-10-22T10:00:00Z');
    expect(snoozedUntil(s, 'alpha', now)?.toISOString()).toBe('2026-10-22T10:00:00.000Z');
    expect(snoozedUntil(s, 'alpha', Date.parse('2026-10-22T10:00:00Z'))).toBeNull();
    expect(snoozedUntil(setSnooze(s, 'alpha', null), 'alpha', now)).toBeNull();
    expect(snoozedUntil(setSnooze(BASE, 'x', 'garbage'), 'x', now)).toBeNull();
  });

  it('lists members and projects with saved settings once, sorted', () => {
    expect(matrixProjects(['gamma', 'beta'], BASE)).toEqual(['beta', 'gamma']);
  });
});
