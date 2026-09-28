import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../../store/itervoxStore';
import { SCHEMA_DRIFT_BANNER_THRESHOLD } from '../../store/schemaDrift';

/**
 * CORE-047 — production-visible schema-drift banner. Shown once
 * SCHEMA_DRIFT_BANNER_THRESHOLD consecutive snapshots (SSE, poll or
 * refreshSnapshot) failed to parse, i.e. the dashboard is showing frozen data
 * because this bundle and the daemon disagree on the snapshot shape. Cleared
 * by the next snapshot that parses.
 */
export function SchemaDriftBanner() {
  const { count, drift } = useItervoxStore(
    useShallow((s) => ({ count: s.schemaDriftCount, drift: s.lastSchemaDrift })),
  );
  if (count < SCHEMA_DRIFT_BANNER_THRESHOLD) return null;
  return (
    <div
      role="alert"
      data-testid="schema-drift-banner"
      className="bg-theme-danger-soft text-theme-danger-text border-theme-danger sticky top-0 z-40 border-b px-4 py-2 text-sm"
    >
      <strong className="font-semibold">Dashboard cannot read daemon updates:</strong>{' '}
      <span>
        the last {count} snapshots failed to parse
        {drift ? ` (${drift.reason === 'json' ? 'invalid JSON' : 'schema mismatch'})` : ''}, so what
        you see may be stale. Reload the page; if it persists, the dashboard and daemon versions
        disagree.
      </span>
      {drift?.reason === 'schema' && <span className="ml-2 font-mono text-xs">{drift.detail}</span>}
    </div>
  );
}
