// CORE-047 — schema-drift accounting shared by itervoxStore and the banner.

/**
 * CORE-047 — consecutive snapshot parse failures (SSE, poll fallback and
 * refreshSnapshot share one counter) at which the dashboard shows the
 * schema-drift banner and reports the drift to the daemon (CORE-048).
 */
export const SCHEMA_DRIFT_BANNER_THRESHOLD = 3;

/** Why a snapshot could not be used: the body was not JSON, or it failed Zod. */
export type SchemaDriftReason = 'json' | 'schema';
/** Which path received the unparseable snapshot. */
export type SnapshotSource = 'sse' | 'poll' | 'refresh';

export interface SchemaDriftInfo {
  reason: SchemaDriftReason;
  source: SnapshotSource;
  /** Field paths and Zod issue codes only — never snapshot values. */
  detail: string;
  at: number;
}

/**
 * Classifies a snapshot parse failure. Returns null for anything that is not
 * a JSON-syntax or schema failure (transport errors are CORE-023's stale-data
 * banner's concern, not drift).
 */
export function describeSnapshotParseFailure(
  err: unknown,
): { reason: SchemaDriftReason; detail: string } | null {
  if (err instanceof SyntaxError) return { reason: 'json', detail: 'response is not valid JSON' };
  if (
    typeof err === 'object' &&
    err !== null &&
    'issues' in err &&
    Array.isArray((err as { issues: unknown }).issues)
  ) {
    const issues = (err as { issues: { path?: PropertyKey[]; code?: string }[] }).issues;
    const parts = issues.slice(0, 5).map((i) => {
      const path = (i.path ?? []).map((p) => String(p)).join('.') || '(root)';
      return `${path}: ${i.code ?? 'invalid'}`;
    });
    const more = issues.length > 5 ? ` (+${String(issues.length - 5)} more)` : '';
    return { reason: 'schema', detail: parts.join('; ') + more };
  }
  return null;
}
