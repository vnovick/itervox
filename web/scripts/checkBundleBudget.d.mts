export interface BundleChunk {
  name: string;
  size: number;
  content: string;
}
export interface BundleBudget {
  totalJsBytes: number;
  initialJsBytes: number;
  maxChunkBytes: number;
  chunks: { name: string; pattern: RegExp; maxBytes: number; forbid: string[] }[];
  forbidEverywhere: string[];
}
export const BUDGET: BundleBudget;
export function checkBundleBudget(
  chunks: BundleChunk[],
  indexHtml: string,
  budget?: BundleBudget,
): { violations: string[]; totalJs: number; initialJs: number };
export function readDist(distDir: string): { chunks: BundleChunk[]; indexHtml: string };
