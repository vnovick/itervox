// Returns the total count of items rendered in the Notifications tab.
// Memoised against the same `snapshot` + `issues` inputs that
// `NotificationsView` consumes so both surfaces stay in sync without a
// duplicate derivation.

import { useMemo } from 'react';
import { useItervoxStore } from '../../../store/itervoxStore';
import { buildOperatorQueueItems } from '../../../lib/operatorQueue';
import type { TrackerIssue } from '../../../types/schemas';

export function useNotificationsTotal(issues: readonly TrackerIssue[]): number {
  const snapshot = useItervoxStore((s) => s.snapshot);
  return useMemo(() => {
    if (!snapshot) return 0;
    // CORE-078 — the attention count (read-only resuming rows excluded), so
    // the tab label matches the inbox, the nav badge and the page title.
    return buildOperatorQueueItems(snapshot, issues).attention;
  }, [snapshot, issues]);
}
