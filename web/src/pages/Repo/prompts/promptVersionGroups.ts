// Groups prompt versions (#186) by the profile that ran them, for Repo
// Analysis → Prompt versions (#200). A version run by several profiles is
// listed under each; one with no recorded profile goes under NO_PROFILE.
// Within a profile, versions keep the server's order: newest last-seen first.

import type { PromptVersionRow } from '../../../types/schemas';

export const NO_PROFILE = '(no profile)';

export interface PromptVersionGroup {
  profile: string;
  versions: PromptVersionRow[];
}

export function groupPromptVersionsByProfile(
  versions: readonly PromptVersionRow[],
): PromptVersionGroup[] {
  const byProfile = new Map<string, PromptVersionRow[]>();
  for (const v of versions) {
    const profiles = v.profiles && v.profiles.length > 0 ? v.profiles : [NO_PROFILE];
    for (const p of profiles) {
      const list = byProfile.get(p) ?? [];
      list.push(v);
      byProfile.set(p, list);
    }
  }
  return [...byProfile.entries()]
    .map(([profile, list]) => ({ profile, versions: list }))
    .sort((a, b) => {
      if (a.profile === NO_PROFILE) return 1;
      if (b.profile === NO_PROFILE) return -1;
      return a.profile.localeCompare(b.profile);
    });
}

/** "73%" for a 0–1 rate. */
export function fmtRate(rate: number): string {
  return `${String(Math.round(rate * 100))}%`;
}

/** Local date and time, minutes precision. */
export function fmtSeen(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? iso
    : d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}
