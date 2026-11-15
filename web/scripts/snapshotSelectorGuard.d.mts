export const ALLOWED_FILES: string[];
export function findWholeSnapshotSelectors(
  source: string,
  fileName?: string,
): { line: number; reason: string; text: string }[];
export function scanSourceTree(
  root: string,
): { file: string; line: number; reason: string; text: string }[];
