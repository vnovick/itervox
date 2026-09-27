import { useEffect, useMemo, useRef } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useItervoxStore } from '../store/itervoxStore';
import { ISSUES_KEY } from '../queries/issues';
import { logIdentifiersKey } from '../queries/logs';
import { inputRequiredFingerprintValue } from '../utils/inputRequired';
import type { StateSnapshot } from '../types/schemas';

export function buildSnapshotInvalidationFingerprint(
  snapshot: StateSnapshot | null,
): string | null {
  if (!snapshot) return null;
  const sortStrings = (values: readonly string[]) => [...values].sort((a, b) => a.localeCompare(b));
  return JSON.stringify({
    running: sortStrings(snapshot.running.map((row) => row.identifier)),
    retrying: sortStrings(snapshot.retrying.map((row) => row.identifier)),
    paused: sortStrings(snapshot.paused),
    pausedWithPR: snapshot.pausedWithPR ?? {},
    inputRequired: sortStrings(
      (snapshot.inputRequired ?? []).map((entry) => inputRequiredFingerprintValue(entry)),
    ),
  });
}

/**
 * Invalidates the issues cache whenever the orchestrator's activity fingerprint
 * changes (sessions start, stop, pause, enter input-required, or pick up PR metadata).
 * This bridges the real-time SSE snapshot to the issues list so the kanban
 * board and issue detail refresh immediately instead of waiting for a stale query.
 *
 * CORE-075 — the hook subscribes to the snapshot reference only and memoizes
 * the (sorting, JSON-building) fingerprint on it, so unrelated store traffic
 * such as log lines and keepalives never rebuilds it.
 */
export function useSnapshotInvalidation() {
  const queryClient = useQueryClient();
  const snapshot = useItervoxStore((s) => s.snapshot);
  const fingerprint = useMemo(() => buildSnapshotInvalidationFingerprint(snapshot), [snapshot]);
  const prevRef = useRef<string | null>(null);

  useEffect(() => {
    if (fingerprint === null) return; // no snapshot yet
    if (prevRef.current !== null && prevRef.current !== fingerprint) {
      void queryClient.invalidateQueries({ queryKey: ISSUES_KEY });
      void queryClient.invalidateQueries({ queryKey: ['issue'] });
      void queryClient.invalidateQueries({ queryKey: logIdentifiersKey() });
    }
    prevRef.current = fingerprint;
  }, [fingerprint, queryClient]);
}
