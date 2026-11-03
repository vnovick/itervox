import type { HubNotificationSettings } from '../types/schemas';

// #229 — pure edits of the hub's notification settings (notifications.json).
// The hub decides routing (internal/hub NotificationSettings.Route); these
// helpers only mirror its "project route, else default" rule for display and
// build the document PUT /hub/api/v1/notifications saves.

/** Display labels of the notification kinds. */
export const KIND_LABELS: Record<string, string> = {
  input_required: 'Input required',
  guardrail_approval: 'Guardrail approval',
  run_failed: 'Run failed',
  review_verdict: 'Review verdict',
  pr_merged: 'PR merged',
  lesson_proposed: 'Lesson proposed',
  dispatch_paused: 'Dispatch paused',
};

/** Display labels of the channels. */
export const CHANNEL_LABELS: Record<string, string> = {
  push: 'Push',
  slack: 'Slack',
  desktop: 'Desktop',
};

/** Snooze choices, in minutes. */
export const SNOOZE_CHOICES: readonly { label: string; minutes: number }[] = [
  { label: '1 hour', minutes: 60 },
  { label: '4 hours', minutes: 240 },
  { label: '1 day', minutes: 1440 },
  { label: '1 week', minutes: 10080 },
];

const NO_CHANNELS: readonly string[] = [];

type ProjectSettings = HubNotificationSettings['projects'][string];

/** rec[key] when key is set (records are indexed without undefined). */
function own<T>(rec: Record<string, T>, key: string): T | undefined {
  return Object.prototype.hasOwnProperty.call(rec, key) ? rec[key] : undefined;
}

/** The channels kind goes to for project: its own route, else the default. */
export function effectiveRoute(
  s: HubNotificationSettings,
  project: string,
  kind: string,
): readonly string[] {
  const routes = own(s.projects, project)?.routes;
  return (routes && own(routes, kind)) ?? own(s.defaults, kind) ?? NO_CHANNELS;
}

/** Turns channel on or off for project's kind (a project route from then on). */
export function toggleRoute(
  s: HubNotificationSettings,
  project: string,
  kind: string,
  channel: string,
  on: boolean,
): HubNotificationSettings {
  const current = effectiveRoute(s, project, kind);
  const next = on
    ? current.includes(channel)
      ? [...current]
      : [...current, channel]
    : current.filter((c) => c !== channel);
  const p: ProjectSettings = own(s.projects, project) ?? {};
  return {
    ...s,
    projects: { ...s.projects, [project]: { ...p, routes: { ...p.routes, [kind]: next } } },
  };
}

/** Snoozes project until `until` (an ISO time), or clears it with null. */
export function setSnooze(
  s: HubNotificationSettings,
  project: string,
  until: string | null,
): HubNotificationSettings {
  const p: ProjectSettings = { ...own(s.projects, project) };
  if (until) p.snooze_until = until;
  else delete p.snooze_until;
  return { ...s, projects: { ...s.projects, [project]: p } };
}

/** The snooze end of project when it is in the future at nowMs. */
export function snoozedUntil(
  s: HubNotificationSettings,
  project: string,
  nowMs: number,
): Date | null {
  const raw = own(s.projects, project)?.snooze_until;
  if (!raw) return null;
  const at = new Date(raw);
  return Number.isNaN(at.getTime()) || at.getTime() <= nowMs ? null : at;
}

/** The projects the matrix shows: members plus any with saved settings. */
export function matrixProjects(members: readonly string[], s: HubNotificationSettings): string[] {
  return [...new Set([...members, ...Object.keys(s.projects)])].sort();
}
