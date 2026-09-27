import { useMemo } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../store/itervoxStore';
import { useIssues } from '../queries/issues';
import {
  buildOperatorQueueItems,
  type OperatorQueueInput,
  type OperatorQueueResult,
} from '../lib/operatorQueue';
import type { TrackerIssue } from '../types/schemas';

const EMPTY_ISSUES: readonly TrackerIssue[] = [];
const EMPTY_QUEUE: OperatorQueueResult = { groups: [], total: 0, attention: 0 };

// CORE-077/078 — the one subscription behind the attention inbox, the nav
// badge and the page-title count. It selects only the branches the queue
// reads (unchanged branches keep their identity across pushes, CORE-074),
// so a timestamp-only push does not recompute or re-render, and it reads the
// tracker issue list because "Ready for review" items come from it.
export function useOperatorQueue(): OperatorQueueResult {
  const { data: issues = EMPTY_ISSUES } = useIssues();
  const b = useItervoxStore(
    useShallow((s) => ({
      has: s.snapshot !== null,
      backendHealth: s.snapshot?.backendHealth,
      completionState: s.snapshot?.completionState,
      configInvalid: s.snapshot?.configInvalid,
      currentAppSessionId: s.snapshot?.currentAppSessionId,
      history: s.snapshot?.history,
      inputRequired: s.snapshot?.inputRequired,
      outboxEntries: s.snapshot?.outboxEntries,
      paused: s.snapshot?.paused,
      pausedWithPR: s.snapshot?.pausedWithPR,
      recentFailures: s.snapshot?.recentFailures,
      failureAcks: s.snapshot?.failureAcks,
      retrying: s.snapshot?.retrying,
      running: s.snapshot?.running,
    })),
  );
  return useMemo(() => {
    if (!b.has || !b.paused || !b.retrying || !b.running) return EMPTY_QUEUE;
    const input: OperatorQueueInput = {
      backendHealth: b.backendHealth,
      completionState: b.completionState,
      configInvalid: b.configInvalid,
      currentAppSessionId: b.currentAppSessionId,
      history: b.history,
      inputRequired: b.inputRequired,
      outboxEntries: b.outboxEntries,
      paused: b.paused,
      pausedWithPR: b.pausedWithPR,
      recentFailures: b.recentFailures,
      failureAcks: b.failureAcks,
      retrying: b.retrying,
      running: b.running,
    };
    return buildOperatorQueueItems(input, issues);
  }, [b, issues]);
}

/** The attention count shown by the inbox heading, nav badge and page title. */
export function useAttentionCount(): number {
  return useOperatorQueue().attention;
}
