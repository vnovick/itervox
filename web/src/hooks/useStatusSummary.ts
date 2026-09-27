import { useMemo } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../store/itervoxStore';
import { summarizeStatus, type StatusSummary } from '../lib/statusModel';

// CORE-076 — the one subscription every status surface uses. Selects only
// the branches summarizeStatus reads (each keeps its identity across
// structurally-shared pushes, CORE-074), so unrelated snapshot changes do
// not re-render the caller.
export function useStatusSummary(): StatusSummary {
  const { hasSnapshot, running, paused, retrying, inputRequired, maxConcurrentAgents, live } =
    useItervoxStore(
      useShallow((s) => ({
        hasSnapshot: s.snapshot !== null,
        running: s.snapshot?.running,
        paused: s.snapshot?.paused,
        retrying: s.snapshot?.retrying,
        inputRequired: s.snapshot?.inputRequired,
        maxConcurrentAgents: s.snapshot?.maxConcurrentAgents,
        live: s.sseConnected,
      })),
    );
  return useMemo(
    () =>
      summarizeStatus(
        hasSnapshot && running && paused && retrying
          ? {
              running,
              paused,
              retrying,
              inputRequired,
              maxConcurrentAgents: maxConcurrentAgents ?? 0,
            }
          : null,
        { sseConnected: live },
      ),
    [hasSnapshot, running, paused, retrying, inputRequired, maxConcurrentAgents, live],
  );
}
