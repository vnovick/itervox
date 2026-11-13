// Command palette ranking: a fuzzy subsequence match. Every character of the
// needle must appear in the haystack in order; the score rewards matches at
// the start of the text and of words (after a separator or a lower→upper
// case change) and runs of consecutive characters, and charges for the
// characters skipped between matches. `wb207` finds `WEB-207`; a needle
// whose characters are scattered thinly through a long text scores at or
// below zero and does not match.

const SCORE_MATCH = 1;
const BONUS_CONSECUTIVE = 2;
const BONUS_WORD_START = 3;
const BONUS_PREFIX = 4;
const BONUS_EXACT = 6;
const PENALTY_GAP = 0.25;
const PENALTY_LEADING = 0.05;
const MAX_LEADING_PENALTY = 2;

function isAlnum(ch: string): boolean {
  return /[\p{L}\p{N}]/u.test(ch);
}

function wordStartBonus(text: string, j: number): number {
  if (j === 0) return BONUS_PREFIX + BONUS_WORD_START;
  const prev = text[j - 1];
  const cur = text[j];
  if (!isAlnum(prev)) return BONUS_WORD_START;
  // camelCase: a lower-case letter followed by an upper-case one.
  if (prev === prev.toLowerCase() && cur !== cur.toLowerCase() && cur === cur.toUpperCase()) {
    return BONUS_WORD_START;
  }
  return 0;
}

/**
 * The best alignment score of `needle` as a subsequence of `haystack`
 * (case-insensitive), or null when it is not a subsequence or the best
 * alignment scores at or below zero.
 */
export function fuzzyScore(needle: string, haystack: string): number | null {
  const q = needle.toLowerCase();
  const t = haystack.toLowerCase();
  const m = q.length;
  const n = t.length;
  if (m === 0) return 0;
  if (m > n) return null;
  if (q === t) return m * (SCORE_MATCH + BONUS_CONSECUTIVE) + BONUS_PREFIX + BONUS_EXACT;

  const NONE = Number.NEGATIVE_INFINITY;
  let prev = new Array<number>(n).fill(NONE);
  for (let j = 0; j < n; j++) {
    if (t[j] === q[0]) {
      prev[j] =
        SCORE_MATCH +
        wordStartBonus(haystack, j) -
        Math.min(j * PENALTY_LEADING, MAX_LEADING_PENALTY);
    }
  }
  for (let i = 1; i < m; i++) {
    const cur = new Array<number>(n).fill(NONE);
    // Best prev[k] + PENALTY_GAP·(k+1) over k ≤ j-2: with a linear gap
    // penalty the charge for a gap from k to j is PENALTY_GAP·(j-k-1).
    let bestGap = NONE;
    for (let j = i; j < n; j++) {
      if (j >= 2) bestGap = Math.max(bestGap, prev[j - 2] + PENALTY_GAP * (j - 1));
      if (t[j] !== q[i]) continue;
      const fromGap = bestGap - PENALTY_GAP * j;
      const fromRun = prev[j - 1] + BONUS_CONSECUTIVE;
      const best = Math.max(fromGap, fromRun);
      if (best === NONE) continue;
      cur[j] = best + SCORE_MATCH + wordStartBonus(haystack, j);
    }
    prev = cur;
  }
  const score = Math.max(...prev);
  return score > 0 ? score : null;
}

/** Lower-case with everything but letters and digits removed (`WEB-207` → `web207`). */
export function normalizeId(text: string): string {
  return text.toLowerCase().replace(/[^\p{L}\p{N}]/gu, '');
}
