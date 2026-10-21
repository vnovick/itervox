// The Repo Analysis page's sections (#200) and their deep-link hashes:
// /repo#capabilities, /repo#usage, /repo#prompts, /repo#readiness.

export const REPO_SECTIONS = [
  { key: 'capabilities', label: 'Capabilities' },
  { key: 'usage', label: 'Runtime usage and cost' },
  { key: 'prompts', label: 'Prompt versions' },
  { key: 'readiness', label: 'Agent readiness' },
] as const;

export type RepoSectionKey = (typeof REPO_SECTIONS)[number]['key'];

export const DEFAULT_REPO_SECTION: RepoSectionKey = 'capabilities';

/** The section a location hash names; the default for anything else. */
export function sectionFromHash(hash: string): RepoSectionKey {
  const key = hash.replace(/^#/, '');
  return REPO_SECTIONS.find((s) => s.key === key)?.key ?? DEFAULT_REPO_SECTION;
}
